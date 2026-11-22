namespace ServiceLib.Tests.Models;

public class ProfileItemSignatureTests
{
    [Test]
    public async Task GetCoreConfigSignature_DisplayOnlyChanges_ShouldStayEqual()
    {
        var node = CreateNode();
        var signature = node.GetCoreConfigSignature();

        node.Remarks = "renamed by the subscription";
        node.DisplayLog = !node.DisplayLog;
        node.IsSub = false;

        await signature.Should().NotBeNull();
        await node.GetCoreConfigSignature().Should().BeEqualTo(signature);
    }

    [Test]
    public async Task GetCoreConfigSignature_ConfigRelevantChanges_ShouldDiffer()
    {
        var signature = CreateNode().GetCoreConfigSignature();

        var addressChanged = CreateNode();
        addressChanged.Address = "10.0.0.9";

        var portChanged = CreateNode();
        portChanged.Port = 24443;

        var passwordChanged = CreateNode();
        passwordChanged.Password = "another-secret";

        var protoExtraChanged = CreateNode();
        protoExtraChanged.ProtoExtra = """{"Flow":"xtls-rprx-vision"}""";

        await (addressChanged.GetCoreConfigSignature() != signature).Should().BeTrue();
        await (portChanged.GetCoreConfigSignature() != signature).Should().BeTrue();
        await (passwordChanged.GetCoreConfigSignature() != signature).Should().BeTrue();
        await (protoExtraChanged.GetCoreConfigSignature() != signature).Should().BeTrue();
    }

    [Test]
    public async Task GetCoreConfigSignature_ConfigStoredOutsideTheNode_ShouldNotCompare()
    {
        // Custom/outbound nodes read their config from a file and group/chain nodes from other rows,
        // so their signature cannot tell whether anything changed and the caller has to be cautious.
        foreach (var configType in new[]
                 {
                     EConfigType.Custom,
                     EConfigType.Outbound,
                     EConfigType.PolicyGroup,
                     EConfigType.ProxyChain,
                 })
        {
            var node = CreateNode();
            node.ConfigType = configType;

            await node.GetCoreConfigSignature().Should().BeNull().Because($"{configType} config is not carried by the node row");
        }
    }

    private static ProfileItem CreateNode()
    {
        return new ProfileItem
        {
            IndexId = "node-zhdata-1",
            ConfigType = EConfigType.ZhData,
            ConfigVersion = 4,
            Subid = string.Empty,
            Remarks = "demo-zhdata",
            Address = "example.com",
            Port = 8443,
            Password = "a-secret",
            Network = "raw",
            StreamSecurity = string.Empty,
            AllowInsecure = "false",
            Sni = string.Empty,
            Fingerprint = string.Empty,
            PublicKey = string.Empty,
            ShortId = string.Empty,
            SpiderX = string.Empty,
            Mldsa65Verify = string.Empty,
            Cert = string.Empty,
            CertSha = string.Empty,
            EchConfigList = string.Empty,
            VerifyPeerCertByName = string.Empty,
            Finalmask = string.Empty,
            ProtoExtra = """{"Servers":["example.com:8443"],"AutoPorts":false}""",
            TransportExtra = """{"RawHeaderType":"none"}""",
        };
    }
}
