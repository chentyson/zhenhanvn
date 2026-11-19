using ServiceLib.ViewModels;

namespace ServiceLib.Handler.SysProxy;

/// <summary>
/// Applies the macOS system proxy through the built-in script. The administrator password is only
/// asked for when the script reports that a real change needs root and sudo cannot supply it
/// silently (<see cref="ProxyScriptContract" />).
/// </summary>
[SupportedOSPlatform("macos")]
public static class ProxySettingOSX
{
    private static readonly string _tag = "ProxySettingOSX";
    private static readonly string _proxySetFileName = $"{Global.ProxySetOSXShellFileName.Replace(Global.NamespaceSample, "")}.sh";

    // One change at a time. Startup, reload and app exit can all call in at once, which used to
    // mean two sudo dialogs (the second throws "DialogHost is already open") and, now that the
    // built-in script is rewritten on every call, a rewrite racing with a running copy.
    private static readonly SemaphoreSlim _gate = new(1, 1);

    public static async Task<bool> SetProxy(string host, int port, string exceptions, bool allowPasswordPrompt = true)
    {
        List<string> args = ["set", host, port.ToString()];
        if (exceptions.IsNotEmpty())
        {
            args.AddRange(exceptions.Split(','));
        }

        return await ExecCmd(args, allowPasswordPrompt);
    }

    public static async Task<bool> UnsetProxy(bool allowPasswordPrompt = true)
    {
        List<string> args = ["clear"];
        return await ExecCmd(args, allowPasswordPrompt);
    }

    private static async Task<bool> ExecCmd(List<string> args, bool allowPasswordPrompt)
    {
        // A shutdown ("best effort") call must not queue up behind a change that is still waiting
        // for a password: it reports "someone else is handling it" instead of blocking the exit.
        if (!await _gate.WaitAsync(allowPasswordPrompt ? Timeout.InfiniteTimeSpan : TimeSpan.Zero))
        {
            Logging.SaveLog($"[{_tag}] another system proxy change is still running, skipped");
            return true;
        }

        try
        {
            return await ExecCmdCore(args, allowPasswordPrompt);
        }
        finally
        {
            _gate.Release();
        }
    }

    private static async Task<bool> ExecCmdCore(List<string> args, bool allowPasswordPrompt)
    {
        var customSystemProxyScriptPath = AppManager.Instance.Config.SystemProxyItem?.CustomSystemProxyScriptPath;
        // The built-in script is rewritten on every call: the cached copy under binConfigs survives
        // in-place updates (the flat-archive update path ditto-merges into Contents/MacOS, and in the
        // LocalAppData fallback it never changes), and a sudo-less copy left over from an older
        // version makes networksetup fail while the script still exits 0 - nothing in the UI, nothing
        // in the log. Users who want their own script use CustomSystemProxyScriptPath.
        var isCustomScript = customSystemProxyScriptPath.IsNotEmpty() && File.Exists(customSystemProxyScriptPath);
        var fileName = isCustomScript
            ? customSystemProxyScriptPath
            : await FileUtils.CreateLinuxShellFile(_proxySetFileName, EmbedUtils.GetEmbedText(Global.ProxySetOSXShellFileName), true);

        // Logged so a support request can tell which script actually ran and with what arguments.
        Logging.SaveLog($"[{_tag}] {fileName} {string.Join(' ', args)}");
        NoticeManager.Instance.SendMessage($"[{_tag}] {fileName}");

        if (isCustomScript)
        {
            // A user-supplied script knows nothing about the "password required" exit code, so with
            // one the password still has to be collected up front (the previous behaviour).
            var customPassword = await GetPasswordAsync(allowPasswordPrompt);
            if (customPassword.IsNullOrEmpty())
            {
                NotifyNoPassword(allowPasswordPrompt);
                return false;
            }

            return await RunWithPasswordAsync(fileName, args, customPassword);
        }

        // First pass without any password. The built-in script compares the current macOS state with
        // what is being asked for, so it exits 0 (no root at all) when nothing has to change - the
        // usual case when v2rayN restarts and macOS still holds the proxy from the last session - and
        // also succeeds without a prompt when sudo is already authenticated.
        var result = await Utils.GetCliWrapResult(fileName, args, null, true);
        if (result.IsSuccess)
        {
            return true;
        }

        if (!ProxyScriptContract.ReportsNeedsPassword(result))
        {
            return ReportFailure(result);
        }

        // A real change is required and sudo cannot do it silently: this is the point where asking
        // the user for the administrator password is justified.
        var password = await GetPasswordAsync(allowPasswordPrompt);
        if (password.IsNullOrEmpty())
        {
            NotifyNoPassword(allowPasswordPrompt);
            return false;
        }

        return await RunWithPasswordAsync(fileName, args, password);
    }

    private static async Task<bool> RunWithPasswordAsync(string fileName, List<string> args, string password)
    {
        // logStandardErrorOnSuccess: a stale or custom script can exit 0 while networksetup failed.
        var result = await Utils.GetCliWrapResult(fileName, args, password, true);
        if (result.IsSuccess)
        {
            return true;
        }

        return ReportFailure(result);
    }

    private static bool ReportFailure(CliRunResult result)
    {
        var detail = result.FailureText();
        Logging.SaveLogError($"[{_tag}] system proxy command failed (exit {result.ExitCode}): {detail}");
        NoticeManager.Instance.SendMessageAndEnqueue($"macOS system proxy was not applied: {detail}");
        return false;
    }

    private static async Task<string?> GetPasswordAsync(bool allowPasswordPrompt)
    {
        if (AppManager.Instance.LinuxSudoPwd.IsNotEmpty())
        {
            return AppManager.Instance.LinuxSudoPwd;
        }

        if (!allowPasswordPrompt)
        {
            // Quietly: the shutdown path already raised the prompt while it still could.
            return null;
        }

        return await SudoSessionManager.PromptForPasswordAsync();
    }

    private static void NotifyNoPassword(bool allowPasswordPrompt)
    {
        string msg;
        if (!allowPasswordPrompt)
        {
            msg = "macOS system proxy needs an administrator password, but no password dialog is shown while the app is closing. Clear the system proxy from the main window, or leave v2rayN running, to keep it applied.";
        }
        else if (!StatusBarViewModel.Instance.ProxyPromptReady)
        {
            msg = "macOS system proxy needs an administrator password, but the password dialog cannot be shown right now (the UI is not ready). Set the system proxy again from the main window to apply it.";
        }
        else
        {
            msg = "macOS system proxy requires an administrator password. No password was provided, so the system proxy was not changed.";
        }

        Logging.SaveLog(_tag, new Exception(msg));
        NoticeManager.Instance.Enqueue(msg);
    }
}
