namespace ServiceLib.Services.CoreConfig;

public class CoreConfigZhDataService(CoreConfigContext context)
{
    private static readonly string _tag = "CoreConfigZhDataService";
    private readonly ProfileItem _node = context.Node;
    private readonly Config _config = context.AppConfig;

    public RetResult GenerateClientConfigContent()
    {
        var ret = new RetResult();
        try
        {
            if (_node == null || !_node.IsValid())
            {
                ret.Msg = ResUI.CheckServerSettings;
                return ret;
            }

            var extra = ZhDataFmt.ParseExtra(_node);
            var servers = ZhDataFmt.GetServers(_node, extra);
            if (servers.Count == 0)
            {
                ret.Msg = ResUI.CheckServerSettings;
                return ret;
            }

            var loglevel = ResolveLogLevel(_config, extra);
            // Without a fronting core zhdata owns the system proxy port. When a routing core fronts
            // it to apply the routing rules, that core owns the system proxy port and zhdata moves
            // to the internal port the front core forwards to (CoreConfigContext.ChainedListenPort).
            var listenPort = context.ChainedListenPort ?? AppManager.Instance.GetLocalPort(EInboundProtocol.socks);
            var config = new ZhDataClientConfig
            {
                Mode = "client",
                Listen = $"{Global.Loopback}:{listenPort}",
                Server = string.Join(",", servers),
                Servers = servers,
                Uuid = _node.Password ?? string.Empty,
                Conns = extra.Conns,
                Spread = extra.Spread,
                DlConns = extra.DlConns,
                Migrate = extra.Migrate,
                SlotBase = extra.SlotBase,
                LogLevel = loglevel,
                Verbose = loglevel.Equals("debug", StringComparison.OrdinalIgnoreCase),
            };

            if (context.ChainedListenPort.HasValue)
            {
                Logging.SaveLog($"{_tag} fronted by a routing core: listening on {listenPort}, the routing rules are applied by the front core");
            }

            ret.Success = true;
            ret.Msg = ResUI.SuccessfulConfiguration;
            ret.Data = JsonUtils.Serialize(config, true);
            return ret;
        }
        catch (Exception ex)
        {
            Logging.SaveLog(_tag, ex);
            ret.Msg = ResUI.FailedGenDefaultConfiguration;
            return ret;
        }
    }

    private static string ResolveLogLevel(Config config, ZhDataExtraItem extra)
    {
        var level = (config.CoreBasicItem.Loglevel ?? string.Empty).Trim().ToLowerInvariant();
        return level switch
        {
            Global.None => Global.None,
            "error" => "error",
            "warning" or "warn" => "warning",
            "info" => "info",
            "debug" => "debug",
            _ => extra.Verbose ? "debug" : "info",
        };
    }
}
