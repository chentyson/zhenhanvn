using CliWrap;
using CliWrap.Buffered;

namespace ServiceLib.Manager;

public class CoreAdminManager
{
    private static readonly Lazy<CoreAdminManager> _instance = new(() => new());
    public static CoreAdminManager Instance => _instance.Value;
    private Config _config;
    private Func<bool, string, Task>? _updateFunc;
    private int _linuxSudoPid = -1;
    private const string _tag = "CoreAdminHandler";

    public async Task Init(Config config, Func<bool, string, Task> updateFunc)
    {
        if (_config != null)
        {
            return;
        }
        _config = config;
        _updateFunc = updateFunc;

        await Task.CompletedTask;
    }

    private async Task UpdateFunc(bool notify, string msg)
    {
        await _updateFunc?.Invoke(notify, msg);
    }

    public async Task<ProcessService?> RunProcessAsLinuxSudo(string fileName, CoreInfo coreInfo, string configPath)
    {
        StringBuilder sb = new();
        sb.AppendLine("#!/bin/bash");

        // The first line of stdin carries the password the app is holding (see ProcessService).
        // When the app holds none, sudo still gets a chance with -n: a valid sudo timestamp or a
        // NOPASSWD sudoers entry starts the core without asking the user anything.
        sb.AppendLine("SUDO_PWD=\"\"");
        sb.AppendLine("IFS= read -r SUDO_PWD || true");
        sb.AppendLine("if [ -n \"$SUDO_PWD\" ]; then");

        var cmdLine = $"{fileName.AppendQuotes()} {string.Format(coreInfo.Arguments, Utils.GetBinConfigPath(configPath).AppendQuotes())}";

        // Passing environment variables to the sudo command, here it only xray or sing-box.
        if (coreInfo.Environment.Count > 0)
        {
            var envArgs = string.Join(" ", coreInfo.Environment.Where(kv => kv.Value.IsNotEmpty()).Select(kv => $"{kv.Key}={kv.Value.AppendQuotes()}"));
            sb.AppendLine($"    printf '%s\\n' \"$SUDO_PWD\" | sudo -S -- env {envArgs} {cmdLine}");
            sb.AppendLine("else");
            sb.AppendLine($"    exec sudo -n -- env {envArgs} {cmdLine}");
        }
        else
        {
            sb.AppendLine($"    printf '%s\\n' \"$SUDO_PWD\" | sudo -S -- {cmdLine}");
            sb.AppendLine("else");
            sb.AppendLine($"    exec sudo -n -- {cmdLine}");
        }

        sb.AppendLine("fi");

        var shFilePath = await FileUtils.CreateLinuxShellFile("run_as_sudo.sh", sb.ToString(), true);

        var procService = new ProcessService(
            fileName: shFilePath,
            arguments: "",
            workingDirectory: Utils.GetBinConfigPath(),
            displayLog: true,
            redirectInput: true,
            environmentVars: null,
            updateFunc: _updateFunc
        );

        await procService.StartAsync(AppManager.Instance.LinuxSudoPwd);

        if (procService is null or { HasExited: true })
        {
            throw new Exception(ResUI.FailedToRunCore);
        }
        _linuxSudoPid = procService.Id;

        return procService;
    }

    public async Task KillProcessAsLinuxSudo()
    {
        if (_linuxSudoPid < 0)
        {
            return;
        }

        try
        {
            var shellFileName = Utils.IsMacOS() ? Global.KillAsSudoOSXShellFileName : Global.KillAsSudoLinuxShellFileName;
            var shFilePath = await FileUtils.CreateLinuxShellFile("kill_as_sudo.sh", EmbedUtils.GetEmbedText(shellFileName), true);
            if (shFilePath.Contains(' '))
            {
                shFilePath = shFilePath.AppendQuotes();
            }
            var arg = new List<string>() { "-c", $"sudo {(AppManager.Instance.LinuxSudoPwd.IsNotEmpty() ? "-S" : "-n")} {shFilePath} {_linuxSudoPid}" };
            var command = Cli.Wrap(Global.LinuxBash).WithArguments(arg);
            if (AppManager.Instance.LinuxSudoPwd.IsNotEmpty())
            {
                command = command.WithStandardInputPipe(PipeSource.FromString(AppManager.Instance.LinuxSudoPwd));
            }
            else
            {
                // No password in this session (passwordless sudo, or the core was started while the
                // sudo timestamp was still valid): let sudo use that credential instead of prompting.
                Logging.SaveLog($"{_tag}: no stored sudo password, killing the core with sudo -n");
            }

            var result = await command.ExecuteBufferedAsync();

            await UpdateFunc(false, result.StandardOutput.ToString());

            await Task.Delay(1000); // Wait for a second to ensure the process is killed
        }
        catch (Exception ex)
        {
            Logging.SaveLog(_tag, ex);
        }

        _linuxSudoPid = -1;
    }
}
