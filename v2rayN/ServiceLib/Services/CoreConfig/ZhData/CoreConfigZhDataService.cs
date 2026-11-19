namespace ServiceLib.Services.CoreConfig;

public class CoreConfigZhDataService(CoreConfigContext context)
{
    private static readonly string _tag = "CoreConfigZhDataService";
    private readonly ProfileItem _node = context.Node;

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

            var config = new ZhDataClientConfig
            {
                Mode = "client",
                Listen = $"{Global.Loopback}:{AppManager.Instance.GetLocalPort(EInboundProtocol.socks)}",
                Server = servers.First(),
                Servers = servers,
                Uuid = _node.Password ?? string.Empty,
                Conns = extra.Conns,
                Spread = extra.Spread,
                DlConns = extra.DlConns,
                Migrate = extra.Migrate,
                SlotBase = extra.SlotBase,
                Verbose = extra.Verbose,
            };

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
}
