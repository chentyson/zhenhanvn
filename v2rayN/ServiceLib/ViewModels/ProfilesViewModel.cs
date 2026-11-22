namespace ServiceLib.ViewModels;

public partial class ProfilesViewModel : MyReactiveObject
{
    public Interaction<string, bool> ShowYesNoInteraction { get; } = new();
    public Interaction<ProfileItem, bool> SaveFileDialogInteraction { get; } = new();
    public Interaction<string, RxVoid> SetClipboardDataInteraction { get; } = new();
    public Interaction<RxVoid, RxVoid> ProfilesFocusInteraction { get; } = new();
    public Interaction<string, RxVoid> ShareServerInteraction { get; } = new();
    public Interaction<RxVoid, RxVoid> DispatcherRefreshServersBizInteraction { get; } = new();
    public Interaction<RxVoid, RxVoid> AdjustMainLvColWidthInteraction { get; } = new();

    public EventChannel<RxVoid> ReloadRequested { get; } = new();
    public EventChannel<RxVoid> RefreshServersRequested { get; } = new();

    #region private prop

    private string _serverFilter = string.Empty;
    private readonly Dictionary<string, bool> _dicHeaderSort = new();
    private SpeedtestService? _speedtestService;
    private string? _pendingSelectIndexId;

    #endregion private prop

    #region ObservableCollection

    public BulkObservableCollection<ProfileItemModel> ProfileItems { get; } = [];

    public BulkObservableCollection<SubItem> SubItems { get; } = [];

    [Reactive]
    public partial ProfileItemModel SelectedProfile { get; set; }

    public IList<ProfileItemModel> SelectedProfiles { get; set; }

    [Reactive]
    public partial SubItem SelectedSub { get; set; }

    [Reactive]
    public partial SubItem SelectedMoveToGroup { get; set; }

    [Reactive]
    public partial string ServerFilter { get; set; }

    #endregion ObservableCollection

    #region Menu

    //servers delete
    public ReactiveCommand<RxVoid, RxVoid> EditServerCmd { get; }

    public ReactiveCommand<RxVoid, RxVoid> RemoveServerCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> RemoveDuplicateServerCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> CopyServerCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> SetDefaultServerCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> ShareServerCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> GenGroupAllServerCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> GenGroupRegionServerCmd { get; }

    //servers move
    public ReactiveCommand<RxVoid, RxVoid> MoveTopCmd { get; }

    public ReactiveCommand<RxVoid, RxVoid> MoveUpCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> MoveDownCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> MoveBottomCmd { get; }
    public ReactiveCommand<SubItem, RxVoid> MoveToGroupCmd { get; }

    //servers ping
    public ReactiveCommand<RxVoid, RxVoid> MixedTestServerCmd { get; }

    public ReactiveCommand<RxVoid, RxVoid> TcpingServerCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> RealPingServerCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> UdpTestServerCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> SpeedServerCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> SortServerResultCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> RemoveInvalidServerResultCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> FastRealPingCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> QueryAccountInfoCmd { get; }

    //servers export
    public ReactiveCommand<RxVoid, RxVoid> Export2ClientConfigCmd { get; }

    public ReactiveCommand<RxVoid, RxVoid> Export2ClientConfigClipboardCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> Export2ShareUrlCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> Export2ShareUrlBase64Cmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> Export2InnerUriCmd { get; }

    public ReactiveCommand<RxVoid, RxVoid> AddSubCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> EditSubCmd { get; }
    public ReactiveCommand<RxVoid, RxVoid> DeleteSubCmd { get; }

    #endregion Menu

    #region Init

    public ProfilesViewModel()
    {
        _config = AppManager.Instance.Config;

        #region WhenAnyValue && ReactiveCommand

        var canEditRemove = this.WhenAnyValue(
           x => x.SelectedProfile,
           selectedSource => selectedSource != null && !selectedSource.IndexId.IsNullOrEmpty());

        this.WhenAnyValue(x => x.SelectedSub)
            .Where(y => y != null && !y.Remarks.IsNullOrEmpty() && _config.SubIndexId != y.Id)
            .SubscribeAsync(async _ => await SubSelectedChangedAsync());
        this.WhenAnyValue(x => x.SelectedMoveToGroup)
            .Where(y => y != null && !y.Remarks.IsNullOrEmpty())
            .SubscribeAsync(async _ => await MoveToGroup());

        this.WhenAnyValue(x => x.ServerFilter)
            .Where(y => y != null && _serverFilter != y)
            .SubscribeAsync(async _ => await ServerFilterChanged());

        //servers delete
        EditServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await EditServerAsync();
        }, canEditRemove);
        RemoveServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await RemoveServerAsync();
        }, canEditRemove);
        RemoveDuplicateServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await RemoveDuplicateServer();
        });
        CopyServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await CopyServer();
        }, canEditRemove);
        SetDefaultServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await SetDefaultServer();
        }, canEditRemove);
        ShareServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await ShareServerAsync();
        }, canEditRemove);
        GenGroupAllServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await GenGroupAllServer();
        }, canEditRemove);
        GenGroupRegionServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await GenGroupRegionServer();
        }, canEditRemove);

        //servers move
        MoveTopCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await MoveServer(EMove.Top);
        }, canEditRemove);
        MoveUpCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await MoveServer(EMove.Up);
        }, canEditRemove);
        MoveDownCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await MoveServer(EMove.Down);
        }, canEditRemove);
        MoveBottomCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await MoveServer(EMove.Bottom);
        }, canEditRemove);
        MoveToGroupCmd = ReactiveCommand.CreateFromTask<SubItem>(async sub =>
        {
            SelectedMoveToGroup = sub;
        });

        //servers ping
        FastRealPingCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await ServerSpeedtest(ESpeedActionType.FastRealping);
        });
        MixedTestServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await ServerSpeedtest(ESpeedActionType.Mixedtest);
        });
        TcpingServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await ServerSpeedtest(ESpeedActionType.Tcping);
        }, canEditRemove);
        RealPingServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await ServerSpeedtest(ESpeedActionType.Realping);
        }, canEditRemove);
        UdpTestServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await ServerSpeedtest(ESpeedActionType.UdpTest);
        }, canEditRemove);
        SpeedServerCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await ServerSpeedtest(ESpeedActionType.Speedtest);
        }, canEditRemove);
        QueryAccountInfoCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await QueryAccountInfo();
        }, canEditRemove);
        SortServerResultCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await SortServer(nameof(EServerColName.DelayVal));
        });
        RemoveInvalidServerResultCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await RemoveInvalidServerResult();
        });
        //servers export
        Export2ClientConfigCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await Export2ClientConfigAsync(false);
        }, canEditRemove);
        Export2ClientConfigClipboardCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await Export2ClientConfigAsync(true);
        }, canEditRemove);
        Export2ShareUrlCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await Export2ShareUrlAsync(false);
        }, canEditRemove);
        Export2ShareUrlBase64Cmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await Export2ShareUrlAsync(true);
        }, canEditRemove);
        Export2InnerUriCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await Export2InnerUrlAsync();
        }, canEditRemove);

        //Subscription
        AddSubCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await EditSubAsync(true);
        });
        EditSubCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await EditSubAsync(false);
        });
        DeleteSubCmd = ReactiveCommand.CreateFromTask(async () =>
        {
            await DeleteSubAsync();
        });

        #endregion WhenAnyValue && ReactiveCommand

        #region AppEvents

        AppEvents.DispatcherStatisticsRequested
            .AsObservable()
            .ObserveOn(RxSchedulers.MainThreadScheduler)
            .SubscribeAsync(async result => await UpdateStatistics(result));

        #endregion AppEvents

        _ = Init();
    }

    private async Task Init()
    {
        SelectedProfile = new();
        SelectedSub = new();
        SelectedMoveToGroup = new();

        await RefreshSubscriptions();
        //await RefreshServers();
    }

    #endregion Init

    #region Actions

    private void Reload()
    {
        ReloadRequested.Publish();
    }

    public async Task SetSpeedTestResult(SpeedTestResult result)
    {
        if (result.IndexId.IsNullOrEmpty())
        {
            NoticeManager.Instance.SendMessageEx(result.Delay);
            NoticeManager.Instance.Enqueue(result.Delay);
            return;
        }
        var item = ProfileItems.FirstOrDefault(it => it.IndexId == result.IndexId);
        if (item == null)
        {
            return;
        }

        if (result.Delay.IsNotEmpty())
        {
            item.Delay = result.Delay.ToInt();
            item.DelayVal = result.Delay ?? string.Empty;
        }
        if (result.Speed.IsNotEmpty())
        {
            item.SpeedVal = result.Speed ?? string.Empty;
        }
        if (result.IpInfo.IsNotEmpty())
        {
            item.IpInfo = result.IpInfo ?? string.Empty;
        }
        await Task.CompletedTask;
    }

    /// <summary>
    /// Queries the s-ui ZY4 account query service for the currently active node
    /// only and fills in its expiry / traffic columns. Other nodes in the list are
    /// not queried. Runs in the background and fails silently (a node without the
    /// endpoint simply stays as it was).
    /// </summary>
    public async Task QueryAccountExpiry()
    {
        try
        {
            var activeIndexId = _config.IndexId;
            if (activeIndexId.IsNullOrEmpty())
            {
                return;
            }

            var node = await AppManager.Instance.GetProfileItem(activeIndexId);
            if (node is null)
            {
                return;
            }

            var data = await AccountQueryService.QueryNodeAsync(node);
            if (data is null)
            {
                // Never overwrite previously stored values with a failed query.
                return;
            }

            ApplyAccountInfo(node, data);
            await ProfileExManager.Instance.SaveTo();

            // The queried node is always the active one, and this query runs
            // only when the service is (re)started — node activation or the
            // "重启服务" reload. If the account has already expired, ask on
            // every such restart whether to open the renewal page.
            if (AccountQueryService.IsAlreadyExpired(data.expiry))
            {
                RxSchedulers.MainThreadScheduler.Schedule(() => _ = ShowExpiredRenewPrompt(node));
            }
        }
        catch
        {
            // Silent: the active node may not expose the query endpoint.
        }
    }

    /// <summary>
    /// Manual query (server list context menu): asks for the account info of every selected
    /// node and reports the outcome, unlike the startup query which stays silent.
    /// </summary>
    private async Task QueryAccountInfo()
    {
        var nodes = await GetProfileItems(true);
        if (nodes is null || nodes.Count == 0)
        {
            NoticeManager.Instance.Enqueue(ResUI.PleaseSelectServer);
            return;
        }

        var updated = 0;
        foreach (var node in nodes)
        {
            var data = await AccountQueryService.QueryNodeAsync(node);
            if (data is null)
            {
                // Keep whatever was stored before: a node without the endpoint simply stays as it was.
                continue;
            }

            ApplyAccountInfo(node, data);
            updated++;
        }
        await ProfileExManager.Instance.SaveTo();

        NoticeManager.Instance.Enqueue(updated > 0
            ? string.Format(ResUI.MsgQueryAccountInfoResult, updated)
            : ResUI.MsgQueryAccountInfoFailed);
    }

    /// <summary>
    /// Stores one query result and refreshes the matching row of the server list.
    /// </summary>
    private void ApplyAccountInfo(ProfileItem node, AccountQueryData data)
    {
        var expireDate = AccountQueryService.FormatExpireDate(data) ?? "-";
        var trafficInfo = AccountQueryService.FormatTraffic(data);
        var expireLevel = AccountQueryService.GetExpireLevel(data.expiry);
        var trafficVolume = data.unlimited_traffic ? 0 : data.volume;
        var trafficUsed = data.unlimited_traffic ? 0 : data.used;
        var trafficLevel = AccountQueryService.GetTrafficLevel(trafficVolume, trafficUsed);

        ProfileExManager.Instance.SetAccountInfo(node.IndexId, data.expiry, expireDate, trafficInfo, trafficVolume, trafficUsed);

        RxSchedulers.MainThreadScheduler.Schedule(() =>
        {
            var item = ProfileItems.FirstOrDefault(it => it.IndexId == node.IndexId);
            if (item != null)
            {
                item.ExpireDate = expireDate;
                item.ExpireLevel = expireLevel;
                item.TrafficInfo = trafficInfo;
                item.TrafficLevel = trafficLevel;
            }
        });
    }

    /// <summary>
    /// Asks whether the expired active node should be renewed; on yes, opens
    /// the customer service page (https://cs.boosoo.cn/?input=续费&lt;别名&gt;) in the default browser.
    /// </summary>
    private async Task ShowExpiredRenewPrompt(ProfileItem node)
    {
        try
        {
            var confirmed = await ShowYesNoInteraction.HandleSafe(string.Format(ResUI.MsgExpiredRenew, node.Remarks));
            if (!confirmed)
            {
                return;
            }

            var url = $"{Global.AccountRenewUrl}{Utils.UrlEncode($"{Global.AccountRenewKeyword}{node.Remarks}")}";
            Process.Start(new ProcessStartInfo { FileName = url, UseShellExecute = true });
        }
        catch (Exception ex)
        {
            Logging.SaveLog("ExpiredRenewPrompt", ex);
        }
    }

    public async Task UpdateStatistics(ServerSpeedItem update)
    {
        if (!_config.GuiItem.EnableStatistics
            || (update.ProxyUp + update.ProxyDown) <= 0
            || DateTime.Now.Second % 3 != 0)
        {
            return;
        }

        try
        {
            var item = ProfileItems.FirstOrDefault(it => it.IndexId == update.IndexId);
            if (item != null)
            {
                item.TodayDown = Utils.HumanFy(update.TodayDown);
                item.TodayUp = Utils.HumanFy(update.TodayUp);
                item.TotalDown = Utils.HumanFy(update.TotalDown);
                item.TotalUp = Utils.HumanFy(update.TotalUp);
            }
        }
        catch
        {
        }
        await Task.CompletedTask;
    }

    #endregion Actions

    #region Servers && Groups

    private async Task SubSelectedChangedAsync()
    {
        _config.SubIndexId = SelectedSub?.Id;

        await RefreshServers();

        await ProfilesFocusInteraction.HandleSafe(RxVoid.Default);
    }

    private async Task ServerFilterChanged()
    {
        _serverFilter = ServerFilter;
        if (_serverFilter.IsNullOrEmpty())
        {
            await RefreshServers();
        }
    }

    public async Task RefreshServers()
    {
        RefreshServersRequested.Publish();

        // await Task.Delay(200);

        await Task.CompletedTask;
    }

    public async Task RefreshServersBiz()
    {
        var lstModel = await GetProfileItemsEx(_config.SubIndexId, _serverFilter);

        ProfileItems.ReplaceRange(lstModel ?? []);
        if (lstModel?.Count > 0)
        {
            ProfileItemModel? selected = null;
            if (!_pendingSelectIndexId.IsNullOrEmpty())
            {
                selected = lstModel.FirstOrDefault(t => t.IndexId == _pendingSelectIndexId);
                _pendingSelectIndexId = null;
            }
            selected ??= lstModel.FirstOrDefault(t => t.IndexId == _config.IndexId);
            SelectedProfile = selected ?? lstModel.First();
        }

        await DispatcherRefreshServersBizInteraction.HandleSafe(RxVoid.Default);
    }

    public async Task RefreshSubscriptions()
    {
        var subItems = await AppManager.Instance.SubItems();
        subItems.Insert(0, new SubItem { Remarks = ResUI.AllGroupServers });

        SubItems.ReplaceRange(subItems);

        SelectedSub = (_config.SubIndexId.IsNotEmpty()
                        ? subItems.FirstOrDefault(t => t.Id == _config.SubIndexId)
                        : null) ?? subItems.FirstOrDefault();
    }

    public async Task AdjustMainLvColWidth()
    {
        await AdjustMainLvColWidthInteraction.HandleSafe(RxVoid.Default);
    }

    private async Task<List<ProfileItemModel>?> GetProfileItemsEx(string subid, string filter)
    {
        var lstModel = await AppManager.Instance.ProfileModels(_config.SubIndexId, filter);

        await ConfigHandler.SetDefaultServer(_config, lstModel);

        var lstServerStat = (_config.GuiItem.EnableStatistics ? StatisticsManager.Instance.ServerStat : null) ?? [];
        var lstProfileExs = await ProfileExManager.Instance.GetProfileExs();
        lstModel = (from t in lstModel
                    join t2 in lstServerStat on t.IndexId equals t2.IndexId into t2b
                    from t22 in t2b.DefaultIfEmpty()
                    join t3 in lstProfileExs on t.IndexId equals t3.IndexId into t3b
                    from t33 in t3b.DefaultIfEmpty()
                    select new ProfileItemModel
                    {
                        IndexId = t.IndexId,
                        ConfigType = t.ConfigType,
                        Remarks = t.Remarks,
                        Address = t.Address,
                        Port = t.Port,
                        //Security = t.Security,
                        Network = t.Network,
                        StreamSecurity = t.StreamSecurity,
                        Subid = t.Subid,
                        SubRemarks = t.SubRemarks,
                        IsActive = t.IndexId == _config.IndexId,
                        Sort = t33?.Sort ?? 0,
                        Delay = t33?.Delay ?? 0,
                        Speed = t33?.Speed ?? 0,
                        DelayVal = t33?.Delay != 0 ? $"{t33?.Delay}" : string.Empty,
                        SpeedVal = t33?.Speed > 0 ? $"{t33?.Speed}" : t33?.Message ?? string.Empty,
                        IpInfo = t33?.IpInfo ?? string.Empty,
                        ExpireDate = t33?.ExpireDate ?? string.Empty,
                        ExpireLevel = AccountQueryService.GetExpireLevel(t33?.ExpireTime ?? 0),
                        TrafficInfo = t33?.TrafficInfo ?? string.Empty,
                        TrafficLevel = AccountQueryService.GetTrafficLevel(t33?.TrafficVolume ?? 0, t33?.TrafficUsed ?? 0),
                        TodayDown = t22 == null ? "" : Utils.HumanFy(t22.TodayDown),
                        TodayUp = t22 == null ? "" : Utils.HumanFy(t22.TodayUp),
                        TotalDown = t22 == null ? "" : Utils.HumanFy(t22.TotalDown),
                        TotalUp = t22 == null ? "" : Utils.HumanFy(t22.TotalUp)
                    }).OrderBy(t => t.Sort).ToList();

        return lstModel;
    }

    #endregion Servers && Groups

    #region Add Servers

    private async Task<List<ProfileItem>?> GetProfileItems(bool latest)
    {
        var lstSelected = new List<ProfileItem>();
        if (SelectedProfiles == null || SelectedProfiles.Count <= 0)
        {
            return null;
        }

        var orderProfiles = SelectedProfiles?.OrderBy(t => t.Sort);
        if (latest)
        {
            lstSelected.AddRange(await AppManager.Instance.GetProfileItemsOrderedByIndexIds(orderProfiles.Select(sp => sp?.IndexId)));
        }
        else
        {
            lstSelected = JsonUtils.Deserialize<List<ProfileItem>>(JsonUtils.Serialize(orderProfiles));
        }

        return lstSelected;
    }

    public async Task EditServerAsync()
    {
        if (string.IsNullOrEmpty(SelectedProfile?.IndexId))
        {
            return;
        }
        var item = await AppManager.Instance.GetProfileItem(SelectedProfile.IndexId);
        if (item is null)
        {
            NoticeManager.Instance.Enqueue(ResUI.PleaseSelectServer);
            return;
        }
        var eConfigType = item.ConfigType;

        bool? ret = false;
        if (eConfigType is EConfigType.Custom or EConfigType.Outbound)
        {
            var addServer2ViewModel = new AddServer2ViewModel(item);
            ret = await AppManager.Instance.WindowDialog.ShowDialogAsync(addServer2ViewModel);
        }
        else if (eConfigType.IsGroupType())
        {
            var addGroupServerViewModel = new AddGroupServerViewModel(item);
            ret = await AppManager.Instance.WindowDialog.ShowDialogAsync(addGroupServerViewModel);
        }
        else
        {
            var addServerViewModel = new AddServerViewModel(item);
            ret = await AppManager.Instance.WindowDialog.ShowDialogAsync(addServerViewModel);
        }
        if (ret == true)
        {
            await RefreshServers();
            if (item.IndexId == _config.IndexId)
            {
                Reload();
            }
        }
    }

    public async Task RemoveServerAsync()
    {
        var lstSelected = await GetProfileItems(true);
        if (lstSelected == null)
        {
            return;
        }
        if (await ShowYesNoInteraction.HandleSafe(ResUI.RemoveServer) == false)
        {
            return;
        }
        var exists = lstSelected.Exists(t => t.IndexId == _config.IndexId);

        await ConfigHandler.RemoveServers(_config, lstSelected);
        NoticeManager.Instance.Enqueue(ResUI.OperationSuccess);
        if (lstSelected.Count == ProfileItems.Count)
        {
            ProfileItems.Clear();
        }
        await RefreshServers();
        if (exists)
        {
            Reload();
        }
    }

    private async Task RemoveDuplicateServer()
    {
        if (await ShowYesNoInteraction.HandleSafe(ResUI.RemoveServer) == false)
        {
            return;
        }

        var tuple = await ConfigHandler.DedupServerList(_config, _config.SubIndexId);
        if (tuple.Item1 > 0 || tuple.Item2 > 0)
        {
            await RefreshServers();
            Reload();
        }
        NoticeManager.Instance.Enqueue(string.Format(ResUI.RemoveDuplicateServerResult, tuple.Item1, tuple.Item2));
    }

    private async Task CopyServer()
    {
        var lstSelected = await GetProfileItems(false);
        if (lstSelected == null)
        {
            return;
        }
        if (await ConfigHandler.CopyServer(_config, lstSelected) == 0)
        {
            await RefreshServers();
            NoticeManager.Instance.Enqueue(ResUI.OperationSuccess);
        }
    }

    public async Task SetDefaultServer()
    {
        if (string.IsNullOrEmpty(SelectedProfile?.IndexId))
        {
            return;
        }
        await SetDefaultServer(SelectedProfile.IndexId);
    }

    public async Task SetDefaultServer(string? indexId)
    {
        if (indexId.IsNullOrEmpty())
        {
            return;
        }
        if (indexId == _config.IndexId)
        {
            return;
        }
        var item = await AppManager.Instance.GetProfileItem(indexId);
        if (item is null)
        {
            NoticeManager.Instance.Enqueue(ResUI.PleaseSelectServer);
            return;
        }

        if (await ConfigHandler.SetDefaultServerIndex(_config, indexId) == 0)
        {
            await RefreshServers();
            Reload();
        }
    }

    public async Task ShareServerAsync()
    {
        var item = await AppManager.Instance.GetProfileItem(SelectedProfile.IndexId);
        if (item is null)
        {
            NoticeManager.Instance.Enqueue(ResUI.PleaseSelectServer);
            return;
        }
        var url = FmtHandler.GetShareUri(item);
        if (url.IsNullOrEmpty())
        {
            return;
        }

        await ShareServerInteraction.HandleSafe(url);
    }

    private async Task GenGroupAllServer()
    {
        var ret = await ConfigHandler.AddGroupAllServer(_config, SelectedSub);
        if (ret.Success != true)
        {
            NoticeManager.Instance.Enqueue(ResUI.OperationFailed);
            return;
        }
        _pendingSelectIndexId = ret.Data?.ToString();
        await RefreshServers();
    }

    private async Task GenGroupRegionServer()
    {
        var ret = await ConfigHandler.AddGroupRegionServer(_config, SelectedSub);
        if (ret.Success != true)
        {
            NoticeManager.Instance.Enqueue(ResUI.OperationFailed);
            return;
        }
        var indexIdList = ret.Data as List<string>;
        _pendingSelectIndexId = indexIdList?.FirstOrDefault();
        await RefreshServers();
    }

    public async Task SortServer(string colName)
    {
        if (colName.IsNullOrEmpty())
        {
            return;
        }

        _dicHeaderSort.TryAdd(colName, true);
        _dicHeaderSort.TryGetValue(colName, out var asc);
        if (await ConfigHandler.SortServers(_config, _config.SubIndexId, colName, asc) != 0)
        {
            return;
        }
        _dicHeaderSort[colName] = !asc;
        await RefreshServers();
    }

    public async Task RemoveInvalidServerResult()
    {
        var count = await ConfigHandler.RemoveInvalidServerResult(_config, _config.SubIndexId);
        await RefreshServers();
        NoticeManager.Instance.Enqueue(string.Format(ResUI.RemoveInvalidServerResultTip, count));
    }

    //move server
    private async Task MoveToGroup()
    {
        var lstSelected = await GetProfileItems(true);
        if (lstSelected == null)
        {
            return;
        }

        await ConfigHandler.MoveToGroup(_config, lstSelected, SelectedMoveToGroup.Id);
        NoticeManager.Instance.Enqueue(ResUI.OperationSuccess);

        await RefreshServers();
        SelectedMoveToGroup = null;
        SelectedMoveToGroup = new();
    }

    public async Task MoveServer(EMove eMove)
    {
        var lstProfile = ProfileItems?.Select(t => t.IndexId).ToList() ?? [];
        var index = lstProfile.IndexOf(SelectedProfile.IndexId);
        if (index < 0)
        {
            NoticeManager.Instance.Enqueue(ResUI.PleaseSelectServer);
            return;
        }

        if (await ConfigHandler.MoveServer(_config, lstProfile, index, eMove) == 0)
        {
            await RefreshServers();
        }
    }

    public async Task MoveServerTo(int startIndex, ProfileItemModel targetItem)
    {
        var targetIndex = ProfileItems.IndexOf(targetItem);
        if (startIndex >= 0 && targetIndex >= 0 && startIndex != targetIndex)
        {
            var lstProfile = ProfileItems?.Select(t => t.IndexId).ToList() ?? [];
            if (await ConfigHandler.MoveServer(_config, lstProfile, startIndex, EMove.Position, targetIndex) == 0)
            {
                await RefreshServers();
            }
        }
    }

    public async Task ServerSpeedtest(ESpeedActionType actionType)
    {
        List<ProfileItem>? lstSelected;
        if (actionType is ESpeedActionType.Mixedtest or ESpeedActionType.FastRealping)
        {
            if (actionType == ESpeedActionType.FastRealping)
            {
                actionType = ESpeedActionType.Realping;
            }

            lstSelected = JsonUtils.Deserialize<List<ProfileItem>>(JsonUtils.Serialize(ProfileItems?.OrderBy(t => t.Sort)));
        }
        else
        {
            lstSelected = await GetProfileItems(false);
        }

        if (lstSelected is null || lstSelected.Count <= 0)
        {
            return;
        }

        _speedtestService ??= new SpeedtestService(_config, async (SpeedTestResult result) =>
        {
            RxSchedulers.MainThreadScheduler.Schedule(() =>
            {
                _ = SetSpeedTestResult(result);
            });
            await Task.CompletedTask;
        });
        _speedtestService?.RunLoop(actionType, lstSelected);
    }

    public void ServerSpeedtestStop()
    {
        _speedtestService?.ExitLoop();
    }

    private async Task Export2ClientConfigAsync(bool blClipboard)
    {
        var item = await AppManager.Instance.GetProfileItem(SelectedProfile.IndexId);
        if (item is null)
        {
            NoticeManager.Instance.Enqueue(ResUI.PleaseSelectServer);
            return;
        }

        var (context, validatorResult) = await CoreConfigContextBuilder.Build(_config, item);
        if (NoticeManager.Instance.NotifyValidatorResult(validatorResult) && !validatorResult.Success)
        {
            return;
        }

        if (blClipboard)
        {
            var result = await CoreConfigHandler.GenerateClientConfig(context, null);
            if (result.Success != true)
            {
                NoticeManager.Instance.Enqueue(result.Msg);
            }
            else
            {
                await SetClipboardDataInteraction.HandleSafe((string)result.Data);
                NoticeManager.Instance.SendMessage(ResUI.OperationSuccess);
            }
        }
        else
        {
            await SaveFileDialogInteraction.HandleSafe(item);
        }
    }

    public async Task Export2ClientConfigResult(string fileName, ProfileItem item)
    {
        if (fileName.IsNullOrEmpty())
        {
            return;
        }
        var (context, validatorResult) = await CoreConfigContextBuilder.Build(_config, item);
        if (NoticeManager.Instance.NotifyValidatorResult(validatorResult) && !validatorResult.Success)
        {
            return;
        }
        var result = await CoreConfigHandler.GenerateClientConfig(context, fileName);
        if (result.Success != true)
        {
            NoticeManager.Instance.Enqueue(result.Msg);
        }
        else
        {
            NoticeManager.Instance.SendMessageAndEnqueue(string.Format(ResUI.SaveClientConfigurationIn, fileName));
        }
    }

    public async Task Export2ShareUrlAsync(bool blEncode)
    {
        var lstSelected = await GetProfileItems(true);
        if (lstSelected == null)
        {
            return;
        }

        StringBuilder sb = new();
        foreach (var it in lstSelected)
        {
            var url = FmtHandler.GetShareUri(it);
            if (url.IsNullOrEmpty())
            {
                continue;
            }
            sb.Append(url);
            sb.AppendLine();
        }
        if (sb.Length > 0)
        {
            if (blEncode)
            {
                await SetClipboardDataInteraction.HandleSafe(Utils.Base64Encode(sb.ToString()));
            }
            else
            {
                await SetClipboardDataInteraction.HandleSafe(sb.ToString());
            }
            NoticeManager.Instance.SendMessage(ResUI.BatchExportURLSuccessfully);
        }
    }

    public async Task Export2InnerUrlAsync()
    {
        var lstSelected = await GetProfileItems(true);
        if (lstSelected == null)
        {
            return;
        }

        var result = string.Empty;

        await Task.Run(() =>
        {
            result = InnerFmt.ToUri(lstSelected);
        });

        if (!result.IsNullOrEmpty())
        {
            await SetClipboardDataInteraction.HandleSafe(result);
            NoticeManager.Instance.SendMessage(ResUI.BatchExportURLSuccessfully);
        }
        else
        {
            NoticeManager.Instance.Enqueue(ResUI.OperationFailed);
        }
    }

    #endregion Add Servers

    #region Subscription

    private async Task EditSubAsync(bool blNew)
    {
        SubItem item;
        if (blNew)
        {
            item = new();
        }
        else
        {
            item = await AppManager.Instance.GetSubItem(_config.SubIndexId);
            if (item is null)
            {
                return;
            }
        }
        var subEditViewModel = new SubEditViewModel(item);
        if (await AppManager.Instance.WindowDialog.ShowDialogAsync(subEditViewModel) == true)
        {
            await RefreshSubscriptions();
            await SubSelectedChangedAsync();
        }
    }

    private async Task DeleteSubAsync()
    {
        var item = await AppManager.Instance.GetSubItem(_config.SubIndexId);
        if (item is null)
        {
            return;
        }

        if (await ShowYesNoInteraction.HandleSafe(ResUI.RemoveServer) == false)
        {
            return;
        }
        await ConfigHandler.DeleteSubItem(_config, item.Id);

        await RefreshSubscriptions();
        await SubSelectedChangedAsync();
    }

    #endregion Subscription
}
