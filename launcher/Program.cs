using System;
using System.Diagnostics;
using System.IO;
using System.IO.Compression;
using System.Reflection;

namespace Launcher;

internal static class Program
{
    private static void Debug(string msg)
    {
        try
        {
            var logPath = Path.Combine(
                Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
                "zhenhanvn-launcher-debug.log");
            File.AppendAllText(logPath, $"[{DateTime.Now:HH:mm:ss.fff}] {msg}{Environment.NewLine}");
        }
        catch { }
    }

    private static int Main(string[] args)
    {
        try
        {
            string exePath = Environment.ProcessPath ?? AppContext.BaseDirectory;
            string exeDir = Path.GetDirectoryName(exePath) ?? ".";
            Debug($"exePath={exePath}");
            Debug($"exeDir={exeDir}");
            Debug($"BaseDir={AppContext.BaseDirectory}");

            // Determine target directory: prefer next to the exe, fall back to LocalAppData
            string targetDir = Path.Combine(exeDir, "zhenhanvn");
            bool canWrite = CanWrite(exeDir);
            Debug($"canWrite={canWrite}");
            if (!canWrite)
            {
                targetDir = Path.Combine(
                    Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
                    "zhenhanvn");
            }
            Debug($"targetDir={targetDir}");

            string appExe = Path.Combine(targetDir, "zhenhanvn.exe");

            // Extract if not already present or incomplete
            bool complete = IsComplete(targetDir);
            Debug($"isComplete={complete}");
            if (!complete)
            {
                Directory.CreateDirectory(targetDir);
                Debug("Extracting...");
                ExtractEmbeddedZip(targetDir);
                Debug("Extraction done");
            }

            // Launch the main application
            Debug($"Launching: {appExe}");
            Process.Start(new ProcessStartInfo
            {
                FileName = appExe,
                WorkingDirectory = targetDir,
                UseShellExecute = true
            });

            return 0;
        }
        catch (Exception ex)
        {
            Debug($"ERROR: {ex}");
            Console.Error.WriteLine($"zhenhanvn launcher error: {ex.Message}");
            return 1;
        }
    }

    private static bool CanWrite(string dir)
    {
        try
        {
            string testFile = Path.Combine(dir, $".wtest_{Guid.NewGuid():N}");
            File.WriteAllText(testFile, "t");
            File.Delete(testFile);
            return true;
        }
        catch
        {
            return false;
        }
    }

    private static bool IsComplete(string dir)
    {
        return File.Exists(Path.Combine(dir, "zhenhanvn.exe"))
            && File.Exists(Path.Combine(dir, "zhenhanvn.dll"))
            && File.Exists(Path.Combine(dir, "coreclr.dll"));
    }

    private static void ExtractEmbeddedZip(string targetDir)
    {
        using var stream = Assembly.GetExecutingAssembly().GetManifestResourceStream("app.zip");
        if (stream == null)
            throw new InvalidOperationException("Embedded app.zip resource not found.");

        using var zip = new ZipArchive(stream, ZipArchiveMode.Read);
        zip.ExtractToDirectory(targetDir, overwriteFiles: true);
    }
}
