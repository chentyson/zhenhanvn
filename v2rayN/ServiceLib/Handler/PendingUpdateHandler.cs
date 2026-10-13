using System.Diagnostics;
using System.IO.Compression;

namespace ServiceLib.Handler;

public static class PendingUpdateHandler
{
    private static readonly string _tag = "PendingUpdateHandler";

    /// <summary>
    /// Check if there is a valid pending update (zip exists and version is newer).
    /// </summary>
    public static bool HasPendingUpdate()
    {
        var config = AppManager.Instance.Config;
        var item = config.CheckUpdateItem;
        if (item == null)
        {
            return false;
        }

        if (string.IsNullOrEmpty(item.PendingUpdateZipPath) || !File.Exists(item.PendingUpdateZipPath))
        {
            return false;
        }

        if (Utils.IsPackagedInstall())
        {
            return false;
        }

        // Version guard: only apply if pending version is newer than current
        if (!string.IsNullOrEmpty(item.PendingUpdateVersion))
        {
            var currentVersion = new SemanticVersion(Utils.GetVersionInfo());
            var pendingVersion = new SemanticVersion(item.PendingUpdateVersion);
            if (pendingVersion <= currentVersion)
            {
                return false;
            }
        }

        return true;
    }

    /// <summary>
    /// Apply the pending update: release locked files, extract the new version, and clear the marker.
    /// Returns true if the update was applied successfully.
    /// </summary>
    public static bool TryApplyPendingUpdate()
    {
        var config = AppManager.Instance.Config;
        var item = config.CheckUpdateItem;

        if (item == null || string.IsNullOrEmpty(item.PendingUpdateZipPath) || !File.Exists(item.PendingUpdateZipPath))
        {
            return false;
        }

        if (Utils.IsPackagedInstall())
        {
            return false;
        }

        var zipPath = item.PendingUpdateZipPath;
        var appDir = Utils.StartupPath();

        try
        {
            Logging.SaveLog($"[{_tag}] Applying pending update: {item.PendingUpdateVersion} from {zipPath}");

            // Step 1: Release locked files (rename exe + loaded native DLLs)
            ReleaseLockedFiles(appDir);

            // Step 2: Extract the new version
            ExtractUpdate(zipPath, appDir);

            // Step 3: Clean up the downloaded zip
            try
            {
                File.Delete(zipPath);
            }
            catch { }

            // Step 4: Clear the pending marker
            item.PendingUpdateVersion = null;
            item.PendingUpdateZipPath = null;
            item.PendingUpdateDownloadedAt = 0;
            _ = ConfigHandler.SaveConfig(config);

            Logging.SaveLog($"[{_tag}] Pending update applied successfully.");
            return true;
        }
        catch (Exception ex)
        {
            Logging.SaveLog(_tag, ex);
            return false;
        }
    }

    /// <summary>
    /// Release all files locked by the running process: the main exe and any loaded native DLLs.
    /// On Windows, renaming a loaded file is safe — the process keeps its handle to the file data.
    /// </summary>
    private static void ReleaseLockedFiles(string appDir)
    {
        // Rename the main exe
        var exePath = Utils.GetExePath();
        if (!string.IsNullOrEmpty(exePath) && exePath.StartsWith(appDir, StringComparison.OrdinalIgnoreCase))
        {
            try
            {
                File.Move(exePath, exePath + ".old");
                Logging.SaveLog($"[{_tag}] Renamed exe: {exePath} -> .old");
            }
            catch (Exception ex)
            {
                Logging.SaveLog($"[{_tag}] Failed to rename exe: {ex.Message}");
            }
        }

        // Rename loaded native DLLs in the app directory
        try
        {
            var currentProcess = Process.GetCurrentProcess();
            foreach (ProcessModule module in currentProcess.Modules)
            {
                try
                {
                    var path = module.FileName;
                    if (path.StartsWith(appDir, StringComparison.OrdinalIgnoreCase)
                        && path.EndsWith(".dll", StringComparison.OrdinalIgnoreCase)
                        && !path.EndsWith(".old", StringComparison.OrdinalIgnoreCase))
                    {
                        File.Move(path, path + ".old");
                        Logging.SaveLog($"[{_tag}] Renamed DLL: {Path.GetFileName(path)} -> .old");
                    }
                }
                catch { }
            }
        }
        catch (Exception ex)
        {
            Logging.SaveLog($"[{_tag}] Failed to enumerate modules: {ex.Message}");
        }
    }

    /// <summary>
    /// Extract the update zip into the target directory, stripping the top-level folder
    /// and skipping existing bin/ files (cores are updated by their own flow).
    /// </summary>
    private static void ExtractUpdate(string zipPath, string targetDir)
    {
        using var archive = ZipFile.OpenRead(zipPath);
        var extractedCount = 0;
        var skippedCount = 0;

        foreach (var entry in archive.Entries)
        {
            try
            {
                if (entry.Length == 0)
                {
                    continue;
                }

                var parts = entry.FullName.Split('/');
                if (parts.Length == 1)
                {
                    continue;
                }

                // Strip the top-level folder
                var relativePath = string.Join('/', parts[1..]);
                var outputPath = Path.Combine(targetDir, relativePath.Replace('/', Path.DirectorySeparatorChar));

                // Skip existing bin/ files (cores are managed separately)
                if (relativePath.StartsWith("bin", StringComparison.OrdinalIgnoreCase) && File.Exists(outputPath))
                {
                    skippedCount++;
                    continue;
                }

                var outputDir = Path.GetDirectoryName(outputPath);
                if (!string.IsNullOrEmpty(outputDir))
                {
                    Directory.CreateDirectory(outputDir);
                }

                // Retry logic for potentially locked files
                var success = false;
                for (var i = 0; i < 5; i++)
                {
                    try
                    {
                        entry.ExtractToFile(outputPath, true);
                        success = true;
                        break;
                    }
                    catch
                    {
                        Thread.Sleep(1000 * (i + 1));
                    }
                }

                if (success)
                {
                    extractedCount++;
                }
            }
            catch (Exception ex)
            {
                Logging.SaveLog($"[{_tag}] Failed to extract {entry.FullName}: {ex.Message}");
            }
        }

        Logging.SaveLog($"[{_tag}] Extraction complete: {extractedCount} files extracted, {skippedCount} skipped.");
    }

    /// <summary>
    /// Delete leftover .old files from a previous update.
    /// </summary>
    public static void CleanupOldFiles(string appDir)
    {
        try
        {
            foreach (var file in Directory.GetFiles(appDir, "*.old"))
            {
                try
                {
                    File.Delete(file);
                }
                catch { }
            }
        }
        catch { }
    }
}
