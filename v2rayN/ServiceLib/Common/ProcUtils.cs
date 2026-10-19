namespace ServiceLib.Common;

public static class ProcUtils
{
    private static readonly string _tag = "ProcUtils";

    public static void ProcessStart(string? fileName, string arguments = "")
    {
        _ = ProcessStart(fileName, arguments, null);
    }

    public static int? ProcessStart(string? fileName, string arguments, string? dir)
    {
        if (fileName.IsNullOrEmpty())
        {
            return null;
        }
        try
        {
            if (fileName.Contains(' '))
            {
                fileName = fileName.AppendQuotes();
            }
            if (arguments.Contains(' '))
            {
                arguments = arguments.AppendQuotes();
            }

            Process proc = new()
            {
                StartInfo = new ProcessStartInfo
                {
                    UseShellExecute = true,
                    FileName = fileName,
                    Arguments = arguments,
                    WorkingDirectory = dir ?? string.Empty
                }
            };
            _ = proc.Start();
            return dir is null ? null : proc.Id;
        }
        catch (Exception ex)
        {
            Logging.SaveLog(_tag, ex);
        }
        return null;
    }

    public static bool RebootAsAdmin(bool blAdmin = true)
    {
        try
        {
            if (!Utils.IsWindows())
            {
                // UAC elevation ("runas") only exists on Windows. On macOS/Linux relaunch
                // the app directly; macOS is relaunched through the .app bundle so the new
                // instance gets a proper app context (Dock icon, activation, etc.).
                var bundlePath = Utils.GetMacAppBundlePath();
                if (!bundlePath.IsNullOrEmpty())
                {
                    var openInfo = new ProcessStartInfo
                    {
                        UseShellExecute = false,
                        FileName = "/usr/bin/open",
                        WorkingDirectory = Utils.StartupPath(),
                    };
                    openInfo.ArgumentList.Add("-n");
                    openInfo.ArgumentList.Add(bundlePath);
                    openInfo.ArgumentList.Add("--args");
                    openInfo.ArgumentList.Add(Global.RebootAs);
                    return Process.Start(openInfo) != null;
                }

                var startInfo = new ProcessStartInfo
                {
                    UseShellExecute = false,
                    Arguments = Global.RebootAs,
                    WorkingDirectory = Utils.StartupPath(),
                    FileName = Utils.GetExePath(),
                };
                return Process.Start(startInfo) != null;
            }

            ProcessStartInfo winStartInfo = new()
            {
                UseShellExecute = true,
                Arguments = Global.RebootAs,
                WorkingDirectory = Utils.StartupPath(),
                FileName = Utils.GetExePath().AppendQuotes(),
                Verb = blAdmin ? "runas" : null,
            };
            return Process.Start(winStartInfo) != null;
        }
        catch (Exception ex)
        {
            Logging.SaveLog(_tag, ex);
            return false;
        }
    }
}
