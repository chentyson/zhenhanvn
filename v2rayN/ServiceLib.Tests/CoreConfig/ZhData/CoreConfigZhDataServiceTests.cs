namespace ServiceLib.Tests.CoreConfig.ZhData;

public class CoreConfigZhDataServiceTests
{
    private const int LocalPort = 10808;

    [Test]
    public async Task GenerateClientConfigContent_NoFrontCore_ShouldListenOnSystemProxyPort()
    {
        var config = CoreConfigTestFactory.CreateConfig();
        CoreConfigTestFactory.BindAppManagerConfig(config);
        var node = CreateZhDataNode();
        var context = CoreConfigTestFactory.CreateContext(config, node, ECoreType.zhdata);

        var result = new CoreConfigZhDataService(context).GenerateClientConfigContent();

        await result.Success.Should().BeTrue().Because($"ret msg: {result.Msg}");
        var listen = ParseListen(result);

        await listen.Should().BeEqualTo($"{Global.Loopback}:{LocalPort}");
    }

    [Test]
    public async Task GenerateClientConfigContent_FrontCoreOwnsSystemProxyPort_ShouldListenOnChainPort()
    {
        // A sing-box core fronts zhdata to apply the routing rules, so zhdata must not grab the
        // system proxy port any more; it listens on the internal port the front core forwards to.
        var config = CoreConfigTestFactory.CreateConfig();
        CoreConfigTestFactory.BindAppManagerConfig(config);
        var node = CreateZhDataNode();
        var chainPort = ConfigHandler.GetZhDataChainPort(config);
        var context = CoreConfigTestFactory.CreateContext(config, node, ECoreType.zhdata) with
        {
            ChainedListenPort = chainPort,
        };

        var result = new CoreConfigZhDataService(context).GenerateClientConfigContent();

        await result.Success.Should().BeTrue().Because($"ret msg: {result.Msg}");
        var listen = ParseListen(result);

        await chainPort.Should().BeEqualTo(LocalPort + Global.ChainedCorePortOffset);
        await listen.Should().BeEqualTo($"{Global.Loopback}:{chainPort}");
        await (listen != $"{Global.Loopback}:{LocalPort}").Should().BeTrue();
    }

    [Test]
    public async Task GetZhDataChainPort_ShouldStayBelowMaxPort()
    {
        var config = CoreConfigTestFactory.CreateConfig();
        CoreConfigTestFactory.BindAppManagerConfig(config);

        var chainPort = ConfigHandler.GetZhDataChainPort(config);

        await chainPort.Should().BeEqualTo(LocalPort + Global.ChainedCorePortOffset);
        await (chainPort <= ushort.MaxValue).Should().BeTrue();
    }

    [Test]
    public async Task NeedsRoutingFrontCore_OnlyForNonCustomZhDataNodes()
    {
        var zhNode = CreateZhDataNode();
        await ConfigHandler.NeedsRoutingFrontCore(zhNode, ECoreType.zhdata).Should().BeTrue();

        // A front core is pointless for cores that apply the routing rules themselves.
        var vmessNode = CoreConfigTestFactory.CreateVmessNode(ECoreType.Xray);
        await ConfigHandler.NeedsRoutingFrontCore(vmessNode, ECoreType.Xray).Should().BeFalse();
        await ConfigHandler.NeedsRoutingFrontCore(vmessNode, ECoreType.sing_box).Should().BeFalse();

        var customNode = new ProfileItem
        {
            IndexId = "node-custom-1",
            ConfigType = EConfigType.Custom,
            CoreType = ECoreType.zhdata,
            Remarks = "demo-custom",
            Address = "config.json",
            Port = 0,
            Subid = string.Empty,
        };
        await ConfigHandler.NeedsRoutingFrontCore(customNode, ECoreType.zhdata).Should().BeFalse();
    }

    [Test]
    public async Task GetPreSocksItem_ZhDataNode_ShouldPointToFrontCoreAtChainPort()
    {
        var config = CoreConfigTestFactory.CreateConfig();
        CoreConfigTestFactory.BindAppManagerConfig(config);
        var node = CreateZhDataNode();

        var item = ConfigHandler.GetPreSocksItem(config, node, ECoreType.zhdata);
        var frontCoreType = ConfigHandler.GetRoutingFrontCoreType();

        if (frontCoreType is null)
        {
            // Without a core that can apply routing rules the node keeps its previous behaviour.
            await item.Should().BeNull();
            return;
        }

        await item.Should().NotBeNull();
        await item!.CoreType.Should().BeEqualTo(frontCoreType.Value);
        await (item.CoreType is ECoreType.sing_box or ECoreType.Xray).Should().BeTrue();
        await item.ConfigType.Should().BeEqualTo(EConfigType.SOCKS);
        await item.Address.Should().BeEqualTo(Global.Loopback);
        await item.Port.Should().BeEqualTo(ConfigHandler.GetZhDataChainPort(config));
    }

    [Test]
    public async Task FrontCore_ShouldOwnSystemProxyPortAndForwardToChainPort()
    {
        // The core that fronts zhdata has to take the system proxy port (that is what the system
        // proxy and the routing rules point at) and forward the proxied traffic to the chain port.
        var config = CoreConfigTestFactory.CreateConfig();
        CoreConfigTestFactory.BindAppManagerConfig(config);
        var chainPort = ConfigHandler.GetZhDataChainPort(config);
        var frontNode = new ProfileItem
        {
            ConfigType = EConfigType.SOCKS,
            CoreType = ECoreType.sing_box,
            Remarks = "demo-zhdata-front",
            Address = Global.Loopback,
            Port = chainPort,
            Network = nameof(ETransport.raw),
            StreamSecurity = string.Empty,
            Subid = string.Empty,
        };
        var context = CoreConfigTestFactory.CreateContext(config, frontNode, ECoreType.sing_box);

        var result = new CoreConfigSingboxService(context).GenerateClientConfigContent();

        await result.Success.Should().BeTrue().Because($"ret msg: {result.Msg}");
        var cfg = JsonUtils.Deserialize<SingboxConfig>(result.Data!.ToString());
        await cfg.Should().NotBeNull();

        await cfg!.inbounds.Should().Contain(i =>
            i.type == nameof(EInboundProtocol.mixed)
            && i.listen == Global.Loopback
            && i.listen_port == LocalPort);
        await cfg.outbounds.Should().Contain(o =>
            o.type == Global.ProtocolTypes[EConfigType.SOCKS]
            && o.server == Global.Loopback
            && o.server_port == chainPort);
    }

    [Test]
    public async Task FrontCore_Xray_ShouldOwnSystemProxyPortAndForwardToChainPort()
    {
        // xray is the fallback front core when sing-box is not installed; it has to do the same job.
        var config = CoreConfigTestFactory.CreateConfig(ECoreType.Xray);
        CoreConfigTestFactory.BindAppManagerConfig(config);
        var chainPort = ConfigHandler.GetZhDataChainPort(config);
        var frontNode = CoreConfigTestFactory.CreateSocksNode(ECoreType.Xray, "node-front-1", "demo-zhdata-front");
        frontNode.Address = Global.Loopback;
        frontNode.Port = chainPort;
        frontNode.Username = null;
        frontNode.Password = null;
        var context = CoreConfigTestFactory.CreateContext(config, frontNode, ECoreType.Xray);

        var result = new CoreConfigV2rayService(context).GenerateClientConfigContent();

        await result.Success.Should().BeTrue().Because($"ret msg: {result.Msg}");
        var cfg = JsonUtils.Deserialize<V2rayConfig>(result.Data!.ToString());
        await cfg.Should().NotBeNull();

        var proxyOutbound = cfg!.outbounds.FirstOrDefault(o => o.tag == Global.ProxyTag);
        await proxyOutbound.Should().NotBeNull();
        await cfg.inbounds.Should().Contain(i => i.port == LocalPort);
        await proxyOutbound!.protocol.Should().BeEqualTo(nameof(EConfigType.SOCKS).ToLowerInvariant());
        await proxyOutbound.settings.servers.Should().Contain(s => s.address == Global.Loopback && s.port == chainPort);
    }

    [Test]
    public async Task FrontCore_Xray_ShouldApplyTheBuiltInRoutingRules()
    {
        // Fronting zhdata is only there to apply the routing rules, and those rules are core
        // independent: the built-in bypass-mainland rules have to reach an xray front core too.
        var config = CoreConfigTestFactory.CreateConfig(ECoreType.Xray);
        CoreConfigTestFactory.BindAppManagerConfig(config);
        var chainPort = ConfigHandler.GetZhDataChainPort(config);
        var frontNode = CoreConfigTestFactory.CreateSocksNode(ECoreType.Xray, "node-front-routing", "demo-zhdata-front");
        frontNode.Address = Global.Loopback;
        frontNode.Port = chainPort;
        frontNode.Username = null;
        frontNode.Password = null;

        var routing = new RoutingItem
        {
            Id = "routing-bypass-mainland",
            Remarks = "绕过大陆(Whitelist)",
            Url = string.Empty,
            DomainStrategy = Global.AsIs,
            DomainStrategy4Singbox = string.Empty,
            RuleSet = EmbedUtils.GetEmbedText(Global.CustomRoutingFileName + "white"),
        };

        var context = CoreConfigTestFactory.CreateContext(config, frontNode, ECoreType.Xray) with
        {
            RoutingItem = routing,
        };

        var result = new CoreConfigV2rayService(context).GenerateClientConfigContent();

        await result.Success.Should().BeTrue().Because($"ret msg: {result.Msg}");
        var cfg = JsonUtils.Deserialize<V2rayConfig>(result.Data!.ToString());
        await cfg.Should().NotBeNull();

        // domestic ranges go direct, everything else still falls into the socks outbound that dials zhdata
        await cfg!.routing.rules.Should().Contain(r =>
            r.outboundTag == Global.DirectTag && r.ip?.Contains("geoip:private") == true);
        await cfg.routing.rules.Should().Contain(r =>
            r.outboundTag == Global.DirectTag && r.domain?.Contains("geosite:cn") == true);
        await cfg.routing.rules.Should().Contain(r =>
            r.outboundTag == Global.ProxyTag && r.domain?.Contains("geosite:google") == true);
        await cfg.outbounds.Should().Contain(o => o.tag == Global.DirectTag);
    }

    private static string ParseListen(RetResult result)
    {
        var config = JsonUtils.Deserialize<JsonObject>(result.Data!.ToString());
        return config!["listen"]!.GetValue<string>();
    }

    private static ProfileItem CreateZhDataNode(string indexId = "node-zhdata-1", string remarks = "demo-zhdata")
    {
        return new ProfileItem
        {
            IndexId = indexId,
            ConfigType = EConfigType.ZhData,
            CoreType = ECoreType.zhdata,
            Remarks = remarks,
            Address = "example.com",
            Port = 8443,
            Password = Guid.NewGuid().ToString(),
            Network = string.Empty,
            StreamSecurity = string.Empty,
            Subid = string.Empty,
            ProtoExtra = JsonUtils.Serialize(new ZhDataExtraItem
            {
                Servers = ["example.com:8443"],
                AutoPorts = false,
            }),
        };
    }
}
