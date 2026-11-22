namespace ServiceLib.Tests.CoreConfig.Context;

public class CoreConfigContextBuilderTests
{
    [Test]
    public async Task ResolveNodeAsync_DirectCycleDependency_ShouldFailWithCycleError()
    {
        var config = CoreConfigTestFactory.CreateConfig();
        CoreConfigTestFactory.BindAppManagerConfig(config);

        var groupAId = NewId("group-a");
        var groupBId = NewId("group-b");
        var groupA = CoreConfigTestFactory.CreatePolicyGroupNode(ECoreType.Xray, groupAId, "group-a", [groupBId]);
        var groupB = CoreConfigTestFactory.CreatePolicyGroupNode(ECoreType.Xray, groupBId, "group-b", [groupAId]);

        await UpsertProfilesAsync(groupA, groupB);

        var context = CoreConfigTestFactory.CreateContext(config, groupA, ECoreType.Xray);
        context.AllProxiesMap.Clear();

        var (_, validatorResult) = await CoreConfigContextBuilder.ResolveNodeAsync(context, groupA, false);

        await validatorResult.Success.Should().BeFalse();
        await validatorResult.Errors.Should().Contain(ContainsCycleDependencyMessage);
        await context.AllProxiesMap.Should().NotContainKey(groupA.IndexId);
        await context.AllProxiesMap.Should().NotContainKey(groupB.IndexId);
    }

    [Test]
    public async Task ResolveNodeAsync_IndirectCycleDependency_ShouldFailWithCycleError()
    {
        var config = CoreConfigTestFactory.CreateConfig();
        CoreConfigTestFactory.BindAppManagerConfig(config);

        var groupAId = NewId("group-a");
        var groupBId = NewId("group-b");
        var groupCId = NewId("group-c");
        var groupA = CoreConfigTestFactory.CreatePolicyGroupNode(ECoreType.Xray, groupAId, "group-a", [groupBId]);
        var groupB = CoreConfigTestFactory.CreatePolicyGroupNode(ECoreType.Xray, groupBId, "group-b", [groupCId]);
        var groupC = CoreConfigTestFactory.CreatePolicyGroupNode(ECoreType.Xray, groupCId, "group-c", [groupAId]);

        await UpsertProfilesAsync(groupA, groupB, groupC);

        var context = CoreConfigTestFactory.CreateContext(config, groupA, ECoreType.Xray);
        context.AllProxiesMap.Clear();

        var (_, validatorResult) = await CoreConfigContextBuilder.ResolveNodeAsync(context, groupA, false);

        await validatorResult.Success.Should().BeFalse();
        await validatorResult.Errors.Should().Contain(ContainsCycleDependencyMessage);
        await context.AllProxiesMap.Should().NotContainKey(groupA.IndexId);
        await context.AllProxiesMap.Should().NotContainKey(groupB.IndexId);
        await context.AllProxiesMap.Should().NotContainKey(groupC.IndexId);
    }

    [Test]
    public async Task ResolveNodeAsync_CycleWithValidBranch_ShouldSkipCycleAndKeepValidChild()
    {
        var config = CoreConfigTestFactory.CreateConfig();
        CoreConfigTestFactory.BindAppManagerConfig(config);

        var groupAId = NewId("group-a");
        var groupBId = NewId("group-b");
        var leafId = NewId("leaf");
        var groupA = CoreConfigTestFactory.CreatePolicyGroupNode(ECoreType.Xray, groupAId, "group-a", [groupBId, leafId]);
        var groupB = CoreConfigTestFactory.CreatePolicyGroupNode(ECoreType.Xray, groupBId, "group-b", [groupAId]);
        var leaf = CoreConfigTestFactory.CreateSocksNode(ECoreType.Xray, leafId, "leaf");

        await UpsertProfilesAsync(groupA, groupB, leaf);

        var context = CoreConfigTestFactory.CreateContext(config, groupA, ECoreType.Xray);
        context.AllProxiesMap.Clear();

        var (_, validatorResult) = await CoreConfigContextBuilder.ResolveNodeAsync(context, groupA, false);

        await validatorResult.Success.Should().BeTrue();
        await validatorResult.Errors.Should().BeEmpty();
        await validatorResult.Warnings.Should().Contain(ContainsCycleDependencyMessage);

        await context.AllProxiesMap.Should().ContainKey(leaf.IndexId);
        await context.AllProxiesMap.Should().ContainKey(groupA.IndexId);
        await context.AllProxiesMap.Should().NotContainKey(groupB.IndexId);
        await groupA.GetProtocolExtra().ChildItems.Should().BeEqualTo(leaf.IndexId);
    }

    [Test]
    public async Task BuildAll_TunWithLegacyProtect_ShouldLeaveTheTunToThePreCore()
    {
        // A node that does not run on sing-box hands TUN to the pre-socks core in legacy protect
        // mode: the main core gets no TUN of its own, so that core has to be installed for TUN to
        // come up at all.
        var config = CoreConfigTestFactory.CreateConfig(ECoreType.Xray);
        config.TunModeItem.EnableTun = true;
        config.TunModeItem.EnableLegacyProtect = true;
        CoreConfigTestFactory.BindAppManagerConfig(config);

        var node = CoreConfigTestFactory.CreateVmessNode(ECoreType.Xray, NewId("xray-node"), "xray-node");

        var allResult = await CoreConfigContextBuilder.BuildAll(config, node);

        await allResult.Success.Should().BeTrue().Because(string.Join("; ", allResult.CombinedValidatorResult.Errors));
        await allResult.PreSocksResult.Should().NotBeNull();
        await allResult.PreSocksResult!.Context.RunCoreType.Should().BeEqualTo(ECoreType.sing_box);
        await allResult.MainResult.Context.IsTunEnabled.Should().BeFalse();
    }

    [Test]
    public async Task BuildAll_TunWithDefaultSettings_ShouldLeaveTheTunToTheRunningCore()
    {
        // The default TUN settings must not rely on a core a base install does not ship: the running
        // core keeps the TUN instead of handing it over to a sing-box protect core.
        var config = CoreConfigTestFactory.CreateConfig(ECoreType.Xray);
        config.TunModeItem.EnableTun = true;
        CoreConfigTestFactory.BindAppManagerConfig(config);

        var node = CoreConfigTestFactory.CreateVmessNode(ECoreType.Xray, NewId("xray-node-default"), "xray-node");

        var allResult = await CoreConfigContextBuilder.BuildAll(config, node);

        await config.TunModeItem.EnableLegacyProtect.Should().BeFalse();
        await allResult.Success.Should().BeTrue().Because(string.Join("; ", allResult.CombinedValidatorResult.Errors));
        await allResult.PreSocksResult.Should().BeNull();
        await allResult.MainResult.Context.IsTunEnabled.Should().BeTrue();
    }

    [Test]
    public async Task BuildAll_ZhDataNodeWithTun_ShouldFrontItWithARoutingCoreAndMoveTheListenPort()
    {
        var config = CoreConfigTestFactory.CreateConfig(ECoreType.Xray);
        config.TunModeItem.EnableTun = true;
        CoreConfigTestFactory.BindAppManagerConfig(config);

        var node = CreateZhDataNode();

        var allResult = await CoreConfigContextBuilder.BuildAll(config, node);
        var frontCoreType = ConfigHandler.GetRoutingFrontCoreType();

        if (frontCoreType is null)
        {
            // No core that can apply routing rules: no chain, and zhdata keeps the system proxy port.
            await allResult.PreSocksResult.Should().BeNull();
            await allResult.MainResult.Context.ChainedListenPort.Should().BeNull();
            return;
        }

        await allResult.Success.Should().BeTrue().Because(string.Join("; ", allResult.CombinedValidatorResult.Errors));
        await allResult.PreSocksResult.Should().NotBeNull();

        // The front core takes the system proxy port and the TUN, and zhdata moves to the port that
        // core dials out to.
        await allResult.PreSocksResult!.Context.RunCoreType.Should().BeEqualTo(frontCoreType.Value);
        await allResult.MainResult.Context.IsTunEnabled.Should().BeFalse();
        await allResult.MainResult.Context.ChainedListenPort
            .Should().BeEqualTo(allResult.PreSocksResult.Context.Node.Port);
    }

    private static ProfileItem CreateZhDataNode()
    {
        return new ProfileItem
        {
            IndexId = $"zhdata-{Guid.NewGuid():N}",
            ConfigType = EConfigType.ZhData,
            CoreType = ECoreType.zhdata,
            Remarks = "demo-zhdata",
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

    private static string NewId(string prefix)
    {
        return $"{prefix}-{Guid.NewGuid():N}";
    }

    private static bool ContainsCycleDependencyMessage(string message)
    {
        return message.Contains("cycle dependency", StringComparison.OrdinalIgnoreCase)
               || message.Contains("循环依赖", StringComparison.Ordinal)
               || message.Contains("循環依賴", StringComparison.Ordinal)
               || message.Contains("циклическую зависимость", StringComparison.OrdinalIgnoreCase);
    }

    private static async Task UpsertProfilesAsync(params ProfileItem[] profiles)
    {
        SQLiteHelper.Instance.CreateTable<ProfileItem>();
        foreach (var profile in profiles)
        {
            await SQLiteHelper.Instance.ReplaceAsync(profile);
        }
    }
}
