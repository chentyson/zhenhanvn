using System.Diagnostics;

namespace ServiceLib.Handler;

public static class PendingUpdateHandler
{
    private static readonly string _tag = "PendingUpdateHandler";

    /// <summary>
    /// Check if there is a valid pending update (zip exists in update/ dir and version is newer).
    /// </summary>
    public static bool HasPendingUpdate()
    {
        if (Utils.IsPackagedInstall())
        {
            return false;
        }

        var config = AppManager.Instance.Config;
        var item = config.CheckUpdateItem;

        var zipPath = FindPendingUpdateZip(item);
        if (zipPath == null)
        {
            return false;
        }

        // Update config to point to the found zip
        if (item != null)
        {
            item.PendingUpdateZipPath = zipPath;
            if (string.IsNullOrEmpty(item.PendingUpdateVersion))
            {
                item.PendingUpdateVersion = ExtractVersionFromZipName(zipPath);
            }
        }

        return true;
    }

    /// <summary>
    /// Find a pending update zip in the update/ directory.
    /// Returns the zip path if a newer version is found, null otherwise.
    /// </summary>
    private static string? FindPendingUpdateZip(CheckUpdateItem? item)
    {
        var currentVersion = new SemanticVersion(Utils.GetVersionInfo());
        var updateDir = Utils.GetUpdatePath();

        // First check the config-specified zip
        if (item != null && !string.IsNullOrEmpty(item.PendingUpdateZipPath) && File.Exists(item.PendingUpdateZipPath))
        {
            if (string.IsNullOrEmpty(item.PendingUpdateVersion))
            {
                return item.PendingUpdateZipPath;
            }
            var pendingVersion = new SemanticVersion(item.PendingUpdateVersion);
            if (pendingVersion > currentVersion)
            {
                return item.PendingUpdateZipPath;
            }
        }

        // Scan update/ directory for newer version zips
        if (Directory.Exists(updateDir))
        {
            string? bestZip = null;
            var bestVersion = new SemanticVersion(0, 0, 0);

            foreach (var zipFile in Directory.GetFiles(updateDir, "v2rayN_*.zip"))
            {
                var versionStr = ExtractVersionFromZipName(zipFile);
                if (versionStr == null)
                {
                    continue;
                }

                var version = new SemanticVersion(versionStr);
                if (version > currentVersion && version > bestVersion)
                {
                    bestVersion = version;
                    bestZip = zipFile;
                }
            }

            return bestZip;
        }

        return null;
    }

    /// <summary>
    /// Extract the version from a zip filename like "v2rayN_7.24.9.4.zip".
    /// </summary>
    private static string? ExtractVersionFromZipName(string zipPath)
    {
        var fileName = Path.GetFileName(zipPath);
        if (fileName.StartsWith("v2rayN_") && fileName.EndsWith(".zip"))
        {
            return fileName["v2rayN_".Length..^4];
        }
        return null;
    }

    /// <summary>
    /// Apply the pending update: release locked files, extract the new version, and clear the marker.
    /// On macOS the whole .app bundle is replaced by a detached shell script (the running app
    /// cannot overwrite its own bundle safely), so the caller must exit the app right after this
    /// method returns true; the script relaunches the new version.
    /// Returns true if the update was applied (or the macOS updater was started) successfully.
    /// </summary>
    public static bool TryApplyPendingUpdate()
    {
        if (Utils.IsPackagedInstall())
        {
            return false;
        }

        var config = AppManager.Instance.Config;
        var item = config.CheckUpdateItem;

        var zipPath = FindPendingUpdateZip(item);
        if (zipPath == null)
        {
            return false;
        }

        try
        {
            if (Utils.IsMacOS())
            {
                return TryApplyPendingUpdateMacOS(zipPath, config);
            }

            var appDir = Utils.StartupPath();

            Logging.SaveLog($"[{_tag}] Applying pending update: {item?.PendingUpdateVersion} from {zipPath}");

            // Step 1: Release locked files (rename exe + loaded native DLLs)
            ReleaseLockedFiles(appDir);

            // Step 2: Copy the zip to a temp location, then extract from there.
            // Copying first decouples extraction from any transient lock or path
            // issue on the update/ directory and lets us clean up the original.
            var tempZip = Utils.GetTempPath($"pending_update_{Guid.NewGuid():N}.zip");
            File.Copy(zipPath, tempZip, true);

            bool extracted;
            try
            {
                // Step 3: Extract the new version
                extracted = ExtractUpdate(tempZip, appDir);
            }
            finally
            {
                try { File.Delete(tempZip); } catch { }
            }

            if (!extracted)
            {
                Logging.SaveLog($"[{_tag}] Extraction produced no files; keeping the update zip for retry.");
                return false;
            }

            // Step 4: Clean up the downloaded zip
            try
            {
                File.Delete(zipPath);
            }
            catch { }

            ClearPendingMarker(config, item);

            Logging.SaveLog($"[{_tag}] Pending update applied successfully.");
            return true;
        }
        catch (Exception ex)
        {
            Logging.SaveLog($"[{_tag}] Failed to apply pending update: {ex}");
            return false;
        }
    }

    private static void ClearPendingMarker(Config config, CheckUpdateItem? item)
    {
        // Step 5: Clear the pending marker
        if (item != null)
        {
            item.PendingUpdateVersion = null;
            item.PendingUpdateZipPath = null;
            item.PendingUpdateDownloadedAt = 0;
        }
        _ = ConfigHandler.SaveConfig(config);
    }

    #region macOS app bundle update

    /// <summary>
    /// Start a detached updater script that waits for this process to exit, replaces the
    /// whole .app bundle with the one contained in the downloaded zip, fixes permissions and
    /// the (ad-hoc) code signature, and relaunches the app.
    /// </summary>
    private static bool TryApplyPendingUpdateMacOS(string zipPath, Config config)
    {
        var bundlePath = Utils.GetMacAppBundlePath();
        if (bundlePath.IsNullOrEmpty())
        {
            Logging.SaveLog($"[{_tag}] Not running from a macOS .app bundle; cannot apply update in place.");
            return false;
        }

        try
        {
            var logPath = Utils.GetLogPath("macOSUpdater.log");
            var scriptPath = Path.Combine(Path.GetTempPath(), $"zhenhanvn-updater-{Guid.NewGuid():N}.sh");

            File.WriteAllText(scriptPath, MacUpdaterScript);
            if (!Utils.SetUnixFileMode(scriptPath))
            {
                Logging.SaveLog($"[{_tag}] Failed to mark updater script executable: {scriptPath}");
            }

            var psi = new ProcessStartInfo
            {
                FileName = "/bin/bash",
                UseShellExecute = false,
                CreateNoWindow = true,
                WorkingDirectory = Path.GetDirectoryName(bundlePath) ?? "/tmp",
            };
            psi.ArgumentList.Add(scriptPath);
            psi.ArgumentList.Add(Environment.ProcessId.ToString());
            psi.ArgumentList.Add(zipPath);
            psi.ArgumentList.Add(bundlePath);
            psi.ArgumentList.Add(logPath);

            var process = Process.Start(psi);
            if (process == null)
            {
                Logging.SaveLog($"[{_tag}] Failed to start macOS updater script.");
                return false;
            }

            Logging.SaveLog($"[{_tag}] macOS updater started (pid {process.Id}): {zipPath} -> {bundlePath}");

            // The zip is deleted by the script once the new bundle is in place. Keep the
            // pending marker until the next start: if the script fails the zip stays in the
            // update directory and the user is offered the update again.
            _ = ConfigHandler.SaveConfig(config);
            return true;
        }
        catch (Exception ex)
        {
            Logging.SaveLog($"[{_tag}] Failed to start macOS updater: {ex}");
            return false;
        }
    }

    /// <summary>
    /// Shell script executed after the app exits. Arguments: pid, zip, app bundle, log file.
    /// </summary>
    private const string MacUpdaterScript =
        """
        #!/bin/bash
        # zhenhanvn macOS updater: replaces the .app bundle and relaunches the app.
        # Arguments: <pid> <zip> <app-bundle> <log>
        PID="$1"
        ZIP="$2"
        APP="$3"
        LOG="$4"

        exec >>"$LOG" 2>&1
        echo "=== zhenhanvn updater $(date '+%Y-%m-%d %H:%M:%S') ==="
        echo "pid=$PID"
        echo "zip=$ZIP"
        echo "app=$APP"

        # Wait up to 60 seconds for the running app to exit.
        i=0
        while [ "$i" -lt 120 ]; do
            if ! /bin/kill -0 "$PID" 2>/dev/null; then
                break
            fi
            /bin/sleep 0.5
            i=$((i + 1))
        done
        if /bin/kill -0 "$PID" 2>/dev/null; then
            echo "app is still running, aborting"
            exit 1
        fi

        WORK="$(/usr/bin/mktemp -d "${TMPDIR:-/tmp}/zhenhanvn-update.XXXXXX" 2>/dev/null)"
        if [ -z "$WORK" ] || [ ! -d "$WORK" ]; then
            echo "cannot create temporary directory"
            exit 1
        fi
        cleanup() {
            /bin/rm -rf "$WORK"
        }
        trap cleanup EXIT

        if /usr/bin/ditto -x -k "$ZIP" "$WORK" >/dev/null 2>&1; then
            echo "extracted with ditto"
        else
            echo "ditto failed, trying unzip"
            if ! /usr/bin/unzip -q -o "$ZIP" -d "$WORK" >/dev/null 2>&1; then
                echo "failed to extract $ZIP"
                exit 1
            fi
        fi

        NEW_APP="$(/usr/bin/find "$WORK" -maxdepth 3 -type d -name '*.app' -print 2>/dev/null | /usr/bin/head -n 1)"

        if [ -n "$NEW_APP" ]; then
            PARENT="$(/usr/bin/dirname "$APP")"
            if [ ! -w "$PARENT" ]; then
                echo "no write permission for $PARENT; leaving the archive for manual installation"
                /usr/bin/open -R "$ZIP"
                /usr/bin/open "$APP"
                exit 0
            fi
            OLD="${APP}.old.$$"
            /bin/rm -rf "$OLD" 2>/dev/null
            if ! /bin/mv "$APP" "$OLD" 2>/dev/null; then
                echo "cannot move the old bundle aside"
                /usr/bin/open -R "$ZIP"
                /usr/bin/open "$APP"
                exit 0
            fi
            if /bin/mv "$NEW_APP" "$APP" 2>/dev/null; then
                /bin/rm -rf "$OLD" 2>/dev/null
            elif /bin/cp -R "$NEW_APP" "$APP" 2>/dev/null; then
                /bin/rm -rf "$OLD" 2>/dev/null
            else
                echo "failed to install the new bundle, restoring the old one"
                /bin/rm -rf "$APP" 2>/dev/null
                /bin/mv "$OLD" "$APP" 2>/dev/null
                exit 1
            fi
        else
            # Flat archive fallback: copy the payload into Contents/MacOS.
            SRC="$WORK"
            if [ "$(/usr/bin/find "$WORK" -maxdepth 1 -mindepth 1 2>/dev/null | /usr/bin/wc -l | /usr/bin/tr -d ' ')" = "1" ]; then
                ONLY="$(/usr/bin/find "$WORK" -maxdepth 1 -mindepth 1 -type d 2>/dev/null | /usr/bin/head -n 1)"
                if [ -n "$ONLY" ] && [ ! -e "$ONLY/Contents/MacOS/zhenhanvn" ]; then
                    SRC="$ONLY"
                fi
            fi
            echo "flat archive, copying $SRC into $APP/Contents/MacOS"
            /bin/mkdir -p "$APP/Contents/MacOS"
            if ! /usr/bin/ditto "$SRC/" "$APP/Contents/MacOS/"; then
                echo "failed to copy the update files"
                exit 1
            fi
        fi

        # Restore the executable bits lost by zip tools that do not store Unix modes.
        /bin/chmod +x "$APP/Contents/MacOS/zhenhanvn" 2>/dev/null
        /usr/bin/find "$APP/Contents/MacOS/bin" -type f -exec /bin/chmod +x {} \; 2>/dev/null

        # Drop quarantine attributes and refresh the ad-hoc signature.
        /usr/bin/xattr -dr com.apple.quarantine "$APP" 2>/dev/null
        /usr/bin/codesign --force --deep --sign - "$APP" >/dev/null 2>&1

        echo "update applied successfully"
        /bin/rm -f "$ZIP"
        /usr/bin/open "$APP"
        exit 0
        """;

    #endregion macOS app bundle update

    /// <summary>
    /// Release all files locked by the running process: the main exe and any loaded native DLLs.
    /// On Windows, renaming a loaded file is safe — the process keeps its handle to the file data.
    /// </summary>
    private static void ReleaseLockedFiles(string appDir)
    {
        if (!Utils.IsWindows())
        {
            // Unix allows replacing files that are in use; the macOS path replaces the whole
            // bundle from a separate process instead and never gets here.
            return;
        }

        // Rename the main exe to .last
        var exePath = Utils.GetExePath();
        if (!string.IsNullOrEmpty(exePath) && exePath.StartsWith(appDir, StringComparison.OrdinalIgnoreCase))
        {
            try
            {
                File.Move(exePath, exePath + ".last");
                Logging.SaveLog($"[{_tag}] Renamed exe: {exePath} -> .last");
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
                        && (path.EndsWith(".dll", StringComparison.OrdinalIgnoreCase)
                            || path.EndsWith(".dylib", StringComparison.OrdinalIgnoreCase))
                        && !path.EndsWith(".last", StringComparison.OrdinalIgnoreCase))
                    {
                        File.Move(path, path + ".last");
                        Logging.SaveLog($"[{_tag}] Renamed library: {Path.GetFileName(path)} -> .last");
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
    /// Extract the update zip into the target directory.
    /// Handles both zips with a single top-level folder (stripped) and zips with files at the root.
    /// Skips existing bin/ files (cores are updated by their own flow).
    ///
    /// NOTE: this deliberately uses the platform tar binary (bsdtar) instead of
    /// System.IO.Compression. In this app's single-file bundle the .NET compression
    /// assemblies are not resolvable at runtime (FileNotFoundException for
    /// System.IO.Compression), so every managed zip API fails. Windows ships tar.exe
    /// (Windows 10 1803+) and macOS/Linux ship bsdtar, both can extract zip archives.
    ///
    /// Returns true if at least one file was extracted successfully.
    /// </summary>
    private static bool ExtractUpdate(string zipPath, string targetDir)
    {
        var tarPath = Utils.IsWindows()
            ? Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.System), "tar.exe")
            : "/usr/bin/tar";
        if (!File.Exists(tarPath))
        {
            tarPath = Utils.IsWindows() ? "tar.exe" : "/bin/tar";
        }
        if (!File.Exists(tarPath) && !Utils.IsWindows())
        {
            Logging.SaveLog($"[{_tag}] tar not found; cannot extract update.");
            return false;
        }

        var tempDir = Utils.GetTempPath($"pending_extract_{Guid.NewGuid():N}");
        Directory.CreateDirectory(tempDir);

        try
        {
            // Step 1: extract the zip into a temp folder using tar (bsdtar).
            var psi = new ProcessStartInfo
            {
                FileName = tarPath,
                Arguments = $"-xf \"{zipPath}\" -C \"{tempDir}\"",
                UseShellExecute = false,
                CreateNoWindow = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
            };

            using var process = Process.Start(psi);
            if (process == null)
            {
                Logging.SaveLog($"[{_tag}] Failed to start {tarPath}.");
                return false;
            }

            var stderr = process.StandardError.ReadToEnd();
            process.StandardOutput.ReadToEnd();
            process.WaitForExit();

            if (process.ExitCode != 0)
            {
                Logging.SaveLog($"[{_tag}] tar failed (exit {process.ExitCode}): {stderr}");
                return false;
            }

            // Step 2: determine the source root. If the zip wrapped everything in a single
            // top-level folder, strip it; otherwise use the temp dir directly.
            var topDirs = Directory.GetDirectories(tempDir);
            var topFiles = Directory.GetFiles(tempDir);
            var sourceRoot = (topDirs.Length == 1 && topFiles.Length == 0) ? topDirs[0] : tempDir;

            var extractedCount = 0;
            var skippedCount = 0;

            // Step 3: copy the extracted files into the target directory.
            foreach (var sourceFile in Directory.GetFiles(sourceRoot, "*", SearchOption.AllDirectories))
            {
                var relativePath = Path.GetRelativePath(sourceRoot, sourceFile);
                var outputPath = Path.Combine(targetDir, relativePath);

                // Skip existing bin/ files (cores are managed separately)
                if (relativePath.StartsWith("bin" + Path.DirectorySeparatorChar, StringComparison.OrdinalIgnoreCase)
                    && File.Exists(outputPath))
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
                        File.Copy(sourceFile, outputPath, true);
                        success = true;
                        break;
                    }
                    catch (Exception ex)
                    {
                        Logging.SaveLog($"[{_tag}] Copy attempt {i + 1} failed for {relativePath}: {ex.Message}");
                        Thread.Sleep(1000 * (i + 1));
                    }
                }

                if (success)
                {
                    extractedCount++;
                    Logging.SaveLog($"[{_tag}] Extracted {relativePath} -> {outputPath}");
                }
                else
                {
                    Logging.SaveLog($"[{_tag}] FAILED to extract {relativePath} after 5 attempts.");
                }
            }

            // Restore the executable bit for cores on Unix.
            if (extractedCount > 0 && !Utils.IsWindows())
            {
                try
                {
                    var binDir = Path.Combine(targetDir, "bin");
                    if (Directory.Exists(binDir))
                    {
                        foreach (var file in Directory.GetFiles(binDir, "*", SearchOption.AllDirectories))
                        {
                            Utils.SetUnixFileMode(file);
                        }
                    }
                }
                catch { }
            }

            Logging.SaveLog($"[{_tag}] Extraction complete: {extractedCount} files extracted, {skippedCount} skipped.");
            return extractedCount > 0;
        }
        finally
        {
            try { Directory.Delete(tempDir, true); } catch { }
        }
    }

    /// <summary>
    /// Delete leftover .last files from a previous update.
    /// </summary>
    public static void CleanupOldFiles(string appDir)
    {
        try
        {
            foreach (var file in Directory.GetFiles(appDir, "*.last"))
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
