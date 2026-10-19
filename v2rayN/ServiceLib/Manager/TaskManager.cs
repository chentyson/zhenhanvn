namespace ServiceLib.Manager;

public class TaskManager
{
    private static readonly Lazy<TaskManager> _instance = new(() => new());
    public static TaskManager Instance => _instance.Value;
    private Config _config;
    private Func<bool, string, Task>? _updateFunc;

    public void RegUpdateTask(Config config, Func<bool, string, Task> updateFunc)
    {
        _config = config;
        _updateFunc = updateFunc;

        // Immediately check for a new main program version on startup
        _ = Task.Run(async () =>
        {
            try
            {
                await UpdateTaskRunCheckUpdate();
            }
            catch (Exception ex)
            {
                Logging.SaveLog("StartupCheckUpdate", ex);
            }
        });

        _ = Task.Factory.StartNew(
            ScheduledTasks,
            CancellationToken.None,
            TaskCreationOptions.LongRunning,
            TaskScheduler.Default);
    }

    private async Task ScheduledTasks()
    {
        Logging.SaveLog("Setup Scheduled Tasks");

        var numOfExecuted = 1;
        while (true)
        {
            //1 minute
            await Task.Delay(1000 * 60);

            //Execute once 1 minute
            try
            {
                await UpdateTaskRunSubscription();
            }
            catch (Exception ex)
            {
                Logging.SaveLog("ScheduledTasks - UpdateTaskRunSubscription", ex);
            }

            //Execute once 20 minute
            if (numOfExecuted % 20 == 0)
            {
                //Logging.SaveLog("Execute save config");

                try
                {
                    await ConfigHandler.SaveConfig(_config);
                    await ProfileExManager.Instance.SaveTo();
                }
                catch (Exception ex)
                {
                    Logging.SaveLog("ScheduledTasks - SaveConfig", ex);
                }
            }

            //Execute once 1 hour
            if (numOfExecuted % 60 == 0)
            {
                //Logging.SaveLog("Execute delete expired files");

                FileUtils.DeleteExpiredFiles(Utils.GetBinConfigPath(), DateTime.Now.AddHours(-1), "Test");
                FileUtils.DeleteExpiredFiles(Utils.GetLogPath(), DateTime.Now.AddDays(-7));
                FileUtils.DeleteExpiredFiles(Utils.GetTempPath(), DateTime.Now.AddDays(-7));

                try
                {
                    await UpdateTaskRunGeo(numOfExecuted / 60);
                }
                catch (Exception ex)
                {
                    Logging.SaveLog("ScheduledTasks - UpdateTaskRunGeo", ex);
                }
            }

            //Execute once 24 hour (skip first iteration — already done on startup)
            if (numOfExecuted > 1 && numOfExecuted % 1440 == 0)
            {
                try
                {
                    await UpdateTaskRunCheckUpdate();
                }
                catch (Exception ex)
                {
                    Logging.SaveLog("ScheduledTasks - UpdateTaskRunCheckUpdate", ex);
                }
            }
            numOfExecuted++;
        }
    }

    private async Task UpdateTaskRunSubscription()
    {
        var updateTime = ((DateTimeOffset)DateTime.Now).ToUnixTimeSeconds();
        var lstSubs = (await AppManager.Instance.SubItems())?
            .Where(t => t.AutoUpdateInterval > 0)
            .Where(t => updateTime - t.UpdateTime >= t.AutoUpdateInterval * 60)
            .ToList();

        if (lstSubs is not { Count: > 0 })
        {
            return;
        }

        Logging.SaveLog("Execute update subscription");

        foreach (var item in lstSubs)
        {
            await SubscriptionHandler.UpdateProcess(_config, item.Id, true, async (success, msg) =>
            {
                await _updateFunc?.Invoke(success, msg);
                if (success)
                {
                    Logging.SaveLog($"Update subscription end. {msg}");
                }
            });
            item.UpdateTime = updateTime;
            await ConfigHandler.AddSubItem(_config, item);
            await Task.Delay(1000);
        }
    }

    private async Task UpdateTaskRunGeo(int hours)
    {
        if (_config.GuiItem.AutoUpdateInterval > 0 && hours > 0 && hours % _config.GuiItem.AutoUpdateInterval == 0)
        {
            Logging.SaveLog("Execute update geo files");

            await new UpdateService(_config, async (success, msg) =>
            {
                await _updateFunc?.Invoke(false, msg);
            }).UpdateGeoFileAll();
        }
    }

    private async Task UpdateTaskRunCheckUpdate()
    {
        Logging.SaveLog("Execute check update");

        // Auto-check and auto-download only the main program (zhenhanvn).
        // Other components (Xray, mihomo, sing_box, GeoFiles) require manual "check update".
        if (Utils.IsPackagedInstall())
        {
            return;
        }

        try
        {
            Logging.SaveLog("AutoDownloadUpdate: checking for GUI update");
            var updateService = new UpdateService(_config, _updateFunc);
            var newVersion = await updateService.DownloadGuiUpdate(
                _config.CheckUpdateItem.CheckPreReleaseUpdate,
                _config.CheckUpdateItem.UpdateViaProxy,
                _updateFunc);

            if (newVersion != null)
            {
                var zipPath = Utils.GetUpdatePath($"v2rayN_{newVersion}.zip");
                _config.CheckUpdateItem.PendingUpdateVersion = newVersion;
                _config.CheckUpdateItem.PendingUpdateZipPath = zipPath;
                _config.CheckUpdateItem.PendingUpdateDownloadedAt = DateTimeOffset.UtcNow.ToUnixTimeSeconds();
                await ConfigHandler.SaveConfig(_config);
                Logging.SaveLog($"AutoDownloadUpdate: downloaded v{newVersion} to {zipPath}");

                // Notify user about the new version
                var msg = string.Format(ResUI.MsgCheckUpdateHasNewVersion, ECoreType.zhenhanvn, newVersion);
                NoticeManager.Instance.Enqueue(msg);
                AppEvents.HasUpdateNotified.Publish(true);

                // Ask the user to update immediately now that the new version has been downloaded.
                AppEvents.UpdateNowRequested.Publish(newVersion);
            }
        }
        catch (Exception ex)
        {
            Logging.SaveLog("AutoDownloadUpdate", ex);
        }
    }
}
