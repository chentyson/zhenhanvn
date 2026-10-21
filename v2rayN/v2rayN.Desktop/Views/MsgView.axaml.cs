using v2rayN.Desktop.Common;

namespace v2rayN.Desktop.Views;

public partial class MsgView : ReactiveUserControl<MsgViewModel>
{
    //private const int KeepLines = 30;

    public MsgView()
    {
        InitializeComponent();
        txtMsg.TextArea.TextView.Options.EnableHyperlinks = false;

        this.WhenActivated(disposables =>
        {
            this.Bind(ViewModel, vm => vm.MsgFilter, v => v.cmbMsgFilter.Text).DisposeWith(disposables);
            this.Bind(ViewModel, vm => vm.AutoRefresh, v => v.togAutoRefresh.IsChecked).DisposeWith(disposables);

            ViewModel.DispatcherShowMsgInteraction.RegisterHandler(interaction =>
            {
                var msg = interaction.Input;
                Dispatcher.UIThread.Post(() => ShowMsg(msg),
                    DispatcherPriority.ApplicationIdle);
                interaction.SetOutput(RxVoid.Default);
            }).DisposeWith(disposables);

            ViewModel?.FlushQueueMsg();
        });

        TextEditorKeywordHighlighter.Attach(txtMsg, Global.LogLevelColors.ToDictionary(
                kv => kv.Key,
                kv => (IBrush)new SolidColorBrush(Color.Parse(kv.Value))
            ));
    }

    private void ShowMsg(object msg)
    {
        //var lineCount = txtMsg.LineCount;
        //if (lineCount > ViewModel?.NumMaxMsg)
        //{
        //    var cutLine = txtMsg.Document.GetLineByNumber(lineCount - KeepLines);
        //    txtMsg.Document.Remove(0, cutLine.Offset);
        //}
        if (txtMsg.LineCount > ViewModel?.NumMaxMsg)
        {
            ClearMsg();
        }

        txtMsg.AppendText(msg.ToString());
        if (togScrollToEnd.IsChecked ?? true)
        {
            txtMsg.ScrollToEnd();
            Dispatcher.UIThread.Invoke(() =>
                    txtMsg.ScrollTo(txtMsg.LineCount, 0, AvaloniaEdit.Rendering.VisualYPosition.TextBottom, txtMsg.Bounds.Height, 0),
                DispatcherPriority.Background);
        }
    }

    public void ClearMsg()
    {
        txtMsg.Clear();
        txtMsg.AppendText("----- Message cleared -----\n");
    }

    private void menuMsgViewSelectAll_Click(object? sender, RoutedEventArgs e)
    {
        Dispatcher.UIThread.Post(() =>
        {
            txtMsg.TextArea.Focus();
            txtMsg.SelectAll();
        }, DispatcherPriority.Render);
    }

    private async void menuMsgViewCopy_Click(object? sender, RoutedEventArgs e)
    {
        var data = txtMsg.SelectedText.TrimEx();
        await AvaUtils.SetClipboardData(this, data);
    }

    private async void menuMsgViewCopyAll_Click(object? sender, RoutedEventArgs e)
    {
        var data = txtMsg.Text.TrimEx();
        await AvaUtils.SetClipboardData(this, data);
    }

    private void menuMsgViewClear_Click(object? sender, RoutedEventArgs e)
    {
        ClearMsg();
    }

    private void btnTroubleshoot_Click(object? sender, RoutedEventArgs e)
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

    private void btnRestoreDefault_Click(object? sender, RoutedEventArgs e)
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
