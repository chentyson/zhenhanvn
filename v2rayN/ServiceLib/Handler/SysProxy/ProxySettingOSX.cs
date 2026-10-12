using ServiceLib.ViewModels;

namespace ServiceLib.Handler.SysProxy;

[SupportedOSPlatform("macos")]
public static class ProxySettingOSX
{
    private static readonly string _proxySetFileName = $"{Global.ProxySetOSXShellFileName.Replace(Global.NamespaceSample, "")}.sh";

    public static async Task SetProxy(string host, int port, string exceptions)
    {
        List<string> args = ["set", host, port.ToString()];
        if (exceptions.IsNotEmpty())
        {
            args.AddRange(exceptions.Split(','));
        }

        await ExecCmd(args);
    }

    public static async Task UnsetProxy()
    {
        List<string> args = ["clear"];
        await ExecCmd(args);
    }

    private static async Task ExecCmd(List<string> args)
    {
        var customSystemProxyScriptPath = AppManager.Instance.Config.SystemProxyItem?.CustomSystemProxyScriptPath;
        var fileName = (customSystemProxyScriptPath.IsNotEmpty() && File.Exists(customSystemProxyScriptPath))
            ? customSystemProxyScriptPath
            : await FileUtils.CreateLinuxShellFile(_proxySetFileName, EmbedUtils.GetEmbedText(Global.ProxySetOSXShellFileName), false);

        // TODO: temporarily notify which script is being used
        NoticeManager.Instance.SendMessage(fileName);

        // networksetup requires root on macOS. Reuse the stored sudo password,
        // or prompt for it once so it can be reused for the session.
        var password = AppManager.Instance.LinuxSudoPwd;
        if (password.IsNullOrEmpty())
        {
            password = await StatusBarViewModel.Instance.PasswordInputInteraction.HandleSafe(RxVoid.Default);
            if (password.IsNotEmpty())
            {
                AppManager.Instance.LinuxSudoPwd = password;
            }
        }

        if (password.IsNullOrEmpty())
        {
            var msg = "macOS system proxy requires an administrator password. No password was provided, so the system proxy was not changed.";
            Logging.SaveLog("ProxySettingOSX", new Exception(msg));
            NoticeManager.Instance.Enqueue(msg);
            return;
        }

        await Utils.GetCliWrapOutput(fileName, args, password);
    }
}
