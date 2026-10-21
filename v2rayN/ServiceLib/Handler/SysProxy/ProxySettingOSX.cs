using ServiceLib.ViewModels;

namespace ServiceLib.Handler.SysProxy;

[SupportedOSPlatform("macos")]
public static class ProxySettingOSX
{
    private static readonly string _proxySetFileName = $"{Global.ProxySetOSXShellFileName.Replace(Global.NamespaceSample, "")}.sh";
    private static readonly SemaphoreSlim _authorizationLock = new(1, 1);

    public static async Task SetProxy(string host, int port, string exceptions)
    {
        List<string> args = ["set", host, port.ToString()];
        if (exceptions.IsNotEmpty())
        {
            args.AddRange(exceptions.Split(','));
        }

        await ExecCmd(args);
    }

    public static async Task UnsetProxy(bool allowPrompt = true)
    {
        List<string> args = ["clear"];
        await ExecCmd(args, allowPrompt);
    }

    public static async Task<bool> EnsureAuthorization(bool allowPrompt = true)
    {
        await _authorizationLock.WaitAsync();
        try
        {
            if (AppManager.Instance.LinuxSudoPwd.IsNotEmpty())
            {
                return true;
            }

            if (!allowPrompt)
            {
                return false;
            }

            var password = await StatusBarViewModel.Instance.PasswordInputInteraction.HandleSafe(RxVoid.Default);
            if (password.IsNotEmpty())
            {
                AppManager.Instance.LinuxSudoPwd = password;
                return true;
            }

            return false;
        }
        finally
        {
            _authorizationLock.Release();
        }
    }

    private static async Task ExecCmd(List<string> args, bool allowPrompt = true)
    {
        var customSystemProxyScriptPath = AppManager.Instance.Config.SystemProxyItem?.CustomSystemProxyScriptPath;
        var fileName = (customSystemProxyScriptPath.IsNotEmpty() && File.Exists(customSystemProxyScriptPath))
            ? customSystemProxyScriptPath
            : await FileUtils.CreateLinuxShellFile(_proxySetFileName, EmbedUtils.GetEmbedText(Global.ProxySetOSXShellFileName), false);

        // TODO: temporarily notify which script is being used
        NoticeManager.Instance.SendMessage(fileName);

        // networksetup requires root on macOS. Reuse the stored sudo password,
        // or prompt for it once so it can be reused for the session.
        await EnsureAuthorization(allowPrompt);
        var password = AppManager.Instance.LinuxSudoPwd;

        if (password.IsNullOrEmpty())
        {
            var msg = allowPrompt
                ? "macOS system proxy requires an administrator password. No password was provided, so the system proxy was not changed."
                : "macOS system proxy could not be restored on exit because no administrator authorization was provided at startup.";
            Logging.SaveLog("ProxySettingOSX", new Exception(msg));
            NoticeManager.Instance.Enqueue(msg);
            return;
        }

        await Utils.GetCliWrapOutput(fileName, args, password);
    }
}
