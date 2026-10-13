namespace v2rayN.Views;

public partial class MsgView
{
    public MsgView()
    {
        InitializeComponent();

        this.WhenActivated(disposables =>
        {
            this.Bind(ViewModel, vm => vm.MsgFilter, v => v.cmbMsgFilter.Text).DisposeWith(disposables);
            this.Bind(ViewModel, vm => vm.AutoRefresh, v => v.togAutoRefresh.IsChecked).DisposeWith(disposables);

            ViewModel.DispatcherShowMsgInteraction.RegisterHandler(interaction =>
            {
                var msg = interaction.Input;
                Application.Current?.Dispatcher.Invoke(() =>
                {
                    ShowMsg(msg);
                }, DispatcherPriority.ApplicationIdle);
                interaction.SetOutput(RxVoid.Default);
            }).DisposeWith(disposables);

            ViewModel?.FlushQueueMsg();
        });

        btnCopy.Click += menuMsgViewCopyAll_Click;
        btnClear.Click += menuMsgViewClear_Click;
        menuMsgViewSelectAll.Click += menuMsgViewSelectAll_Click;
        menuMsgViewCopy.Click += menuMsgViewCopy_Click;
        menuMsgViewCopyAll.Click += menuMsgViewCopyAll_Click;
        menuMsgViewClear.Click += menuMsgViewClear_Click;
        btnTroubleshoot.Click += btnTroubleshoot_Click;
        btnRestoreDefault.Click += btnRestoreDefault_Click;

        cmbMsgFilter.ItemsSource = Global.PresetMsgFilters;
    }

    private void ShowMsg(object msg)
    {
        if (txtMsg.LineCount > ViewModel?.NumMaxMsg)
        {
            ClearMsg();
        }

        txtMsg.AppendText(msg.ToString());
        if (togScrollToEnd.IsChecked ?? true)
        {
            txtMsg.ScrollToEnd();
        }
    }

    public void ClearMsg()
    {
        txtMsg.Clear();
        txtMsg.AppendText("----- Message cleared -----\n");
    }

    private void menuMsgViewSelectAll_Click(object sender, System.Windows.RoutedEventArgs e)
    {
        txtMsg.Focus();
        txtMsg.SelectAll();
    }

    private void menuMsgViewCopy_Click(object sender, System.Windows.RoutedEventArgs e)
    {
        var data = txtMsg.SelectedText.TrimEx();
        WindowsUtils.SetClipboardData(data);
    }

    private void menuMsgViewCopyAll_Click(object sender, System.Windows.RoutedEventArgs e)
    {
        var data = txtMsg.Text;
        WindowsUtils.SetClipboardData(data);
    }

    private void menuMsgViewClear_Click(object sender, System.Windows.RoutedEventArgs e)
    {
        ClearMsg();
    }

    private void btnTroubleshoot_Click(object sender, System.Windows.RoutedEventArgs e)
    {
        var config = AppManager.Instance.Config;
        config.CoreBasicItem.Loglevel = "debug";
        config.SystemProxyItem.SysProxyType = ESysProxyType.ForcedClear;
        config.TunModeItem.EnableTun = false;
        StatusBarViewModel.Instance.SystemProxySelected = (int)ESysProxyType.ForcedClear;
        StatusBarViewModel.Instance.EnableTun = false;
        ClearMsg();
        _ = ConfigHandler.SaveConfig(config);
        StatusBarViewModel.Instance.ReloadRequested.Publish();
    }

    private void btnRestoreDefault_Click(object sender, System.Windows.RoutedEventArgs e)
    {
        var config = AppManager.Instance.Config;
        config.CoreBasicItem.Loglevel = "warning";
        config.SystemProxyItem.SysProxyType = ESysProxyType.ForcedChange;
        config.TunModeItem.EnableTun = false;
        StatusBarViewModel.Instance.SystemProxySelected = (int)ESysProxyType.ForcedChange;
        StatusBarViewModel.Instance.EnableTun = false;
        _ = ConfigHandler.SaveConfig(config);
        StatusBarViewModel.Instance.ReloadRequested.Publish();
    }
}
