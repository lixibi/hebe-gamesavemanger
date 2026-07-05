import {FormEvent, MouseEvent, useEffect, useMemo, useState} from 'react';
import {
    CloudDownload,
    CloudUpload,
    Archive,
    KeyRound,
    FileDown,
    FileUp,
    FolderOpen,
    History,
    Pencil,
    Gamepad2,
    Settings,
    Play,
    Plus,
    RefreshCw,
    RotateCcw as RestoreIcon,
    Save,
    Trash2,
} from 'lucide-react';
import './App.css';
import {
    CompareGame,
    CreateManualBackup,
    DeleteGame,
    ExportGameConfig,
    GetAppState,
    ImportGameConfig,
    LaunchGame,
    ListBackups,
    ListCloudBackups,
    OpenGamePath,
    PickGameExe,
    PickSaveDirectory,
    RestoreBackup,
    RestoreCloudBackup,
    SaveCloudServerURL,
    SaveGame,
    SyncGame,
    TestCloudServerURL,
    ChangeCloudPassword
} from '../wailsjs/go/main/App';
import {main} from '../wailsjs/go/models';
import {EventsOn} from '../wailsjs/runtime/runtime';

type ConfirmState = {
    title: string;
    body: string;
    actions: ConfirmAction[];
};

type ConfirmAction = {
    label: string;
    className: string;
    action: () => Promise<void>;
};

type ContextMenuState = {
    x: number;
    y: number;
    status: main.GameStatus;
};

type BackupMode = 'local' | 'cloud';

type TransferProgress = {
    gameId: string;
    gameName: string;
    direction: string;
    phase: string;
    message: string;
    currentBytes: number;
    totalBytes: number;
    currentPath: string;
    done: boolean;
};

const emptyGame: main.GameConfig = {
    id: '',
    name: '',
    folderName: '',
    localSavePath: '',
    gameExePath: '',
    gameArgs: '',
    autoUploadMode: 'manual',
    autoUploadIntervalMinutes: 5,
    saveSubdir: '',
};

const stateLabels: Record<string, string> = {
    'in-sync': '已同步',
    'cloud-newer': '云端较新',
    'local-newer': '本地较新',
    conflict: '双方有差异',
    'missing-local': '本地缺失',
    'missing-cloud': '云端缺失',
};

const sideLabels: Record<string, string> = {
    local: '推断本地较新',
    cloud: '推断云端较新',
    both: '双方都有变化',
    unknown: '需要人工确认',
};

function App() {
    const [appState, setAppState] = useState<main.AppState | null>(null);
    const [selectedId, setSelectedId] = useState('');
    const [form, setForm] = useState<main.GameConfig>(emptyGame);
    const [busy, setBusy] = useState(false);
    const [notice, setNotice] = useState('');
    const [error, setError] = useState('');
    const [confirm, setConfirm] = useState<ConfirmState | null>(null);
    const [configOpen, setConfigOpen] = useState(false);
    const [contextMenu, setContextMenu] = useState<ContextMenuState | null>(null);
    const [backupOpen, setBackupOpen] = useState(false);
    const [backupMode, setBackupMode] = useState<BackupMode>('local');
    const [cloudConfigOpen, setCloudConfigOpen] = useState(false);
    const [backups, setBackups] = useState<main.BackupInfo[]>([]);
    const [cloudUrl, setCloudUrl] = useState('');
    const [cloudPassword, setCloudPassword] = useState('hebesave');
    const [newCloudPassword, setNewCloudPassword] = useState('');
    const [passwordChangeOpen, setPasswordChangeOpen] = useState(false);
    const [activityLog, setActivityLog] = useState<string[]>(['等待操作']);
    const [compareResult, setCompareResult] = useState<main.CompareResult | null>(null);
    const [transferProgress, setTransferProgress] = useState<TransferProgress | null>(null);

    const selectedStatus = useMemo(() => {
        return appState?.games?.find((item) => item.game.id === selectedId) ?? null;
    }, [appState, selectedId]);

    const cloudSummary = useMemo(() => {
        const items = appState?.games ?? [];
        const synced = items.filter((item) => item.state === 'in-sync').length;
        const attention = items.length - synced;
        return {synced, attention};
    }, [appState?.games]);
    const isOfflineMode = appState?.cloudStatus === 'offline';

    useEffect(() => {
        void refresh();
        const timer = window.setInterval(() => void refresh(false), 5000);
        return () => window.clearInterval(timer);
    }, [selectedId, configOpen]);

    useEffect(() => {
        setCloudUrl(appState?.cloudServerURL ?? '');
        if (appState?.cloudPassword) {
            setCloudPassword(appState.cloudPassword);
        }
    }, [appState?.cloudServerURL, appState?.cloudPassword]);

    useEffect(() => {
        return EventsOn('game-local-newer-after-exit', (payload: main.GameStatus) => {
            const status = main.GameStatus.createFrom(payload);
            setAppState((state) => state ? main.AppState.createFrom({
                ...state,
                games: state.games.map((item) => item.game.id === status.game.id ? status : item),
            }) : state);
            setSelectedId(status.game.id);
            setConfirm({
                title: '游戏已关闭，检测到本地存档更新',
                body: `检测到 ${status.game.name} 的本地存档较新。\n是否现在上传到云端？\n\n本地：${status.game.localSavePath}\n云端：${status.cloudPath}`,
                actions: [
                    {
                        label: '上传本地到云端',
                        className: 'primary',
                        action: () => syncDirection(status.game.id, 'local-to-cloud', false),
                    },
                ],
            });
            appendLog(`${status.game.name} 已关闭，本地存档较新`);
        });
    }, []);

    useEffect(() => {
        return EventsOn('transfer-progress', (payload: TransferProgress) => {
            const progress = payload;
            setTransferProgress(progress);
            if (progress.done) {
                window.setTimeout(() => setTransferProgress(null), 900);
            }
        });
    }, []);

    async function run<T>(task: () => Promise<T>, onSuccess?: (value: T) => void | Promise<void>, showBusy = true) {
        if (showBusy) {
            setBusy(true);
        }
        setError('');
        try {
            const value = await task();
            await onSuccess?.(value);
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err));
        } finally {
            if (showBusy) {
                setBusy(false);
            }
        }
    }

    function appendLog(message: string) {
        const time = new Date().toLocaleTimeString();
        setActivityLog((items) => [`${time} ${message}`, ...items].slice(0, 6));
    }

    async function refresh(showBusy = true) {
        await run(GetAppState, (state) => {
            setAppState(state);
            if (!selectedId && !configOpen && state.games.length > 0) {
                chooseGame(state.games[0]);
            }
        }, showBusy);
    }

    function chooseGame(status: main.GameStatus) {
        setSelectedId(status.game.id);
        setForm({...status.game});
        setCompareResult(null);
        setNotice('');
        setError('');
    }

    function createNewGame() {
        setSelectedId('');
        setForm({...emptyGame});
        setNotice('');
        setError('');
        setConfigOpen(true);
    }

    function editSelectedGame() {
        if (!selectedStatus) {
            return;
        }
        setForm({...selectedStatus.game});
        setConfigOpen(true);
    }

    async function submitGame(event: FormEvent) {
        event.preventDefault();
        const gameIdentifier = (form.folderName || form.name).trim();
        const payload = {
            ...form,
            id: form.id || gameIdentifier,
            folderName: gameIdentifier,
            autoUploadMode: form.autoUploadMode || 'manual',
            autoUploadIntervalMinutes: Math.max(1, Number(form.autoUploadIntervalMinutes || 5)),
            saveSubdir: '',
        };
        await run(() => SaveGame(payload), (state) => {
            setAppState(state);
            const saved = state.games.find((item) => item.game.id === payload.id || item.game.folderName === payload.folderName);
            if (saved) {
                chooseGame(saved);
            }
            setConfigOpen(false);
            if (saved && payload.localSavePath && saved.localFiles === 0) {
                setNotice('配置已保存，但本地存档目录当前是空的，请确认是否指向正确。');
                appendLog(`已保存 ${payload.name} 配置，本地目录为空`);
            } else {
                setNotice('配置已保存');
                appendLog(`已保存 ${payload.name} 配置`);
            }
        });
    }

    function requestSync(direction: 'cloud-to-local' | 'local-to-cloud') {
        if (!selectedStatus) {
            return;
        }
        warnIfCloudUnavailable();
        const title = direction === 'cloud-to-local' ? '云端覆盖本地' : '本地覆盖云端';
        const body = overwriteBody(selectedStatus, direction);

        setConfirm({
            title,
            body,
            actions: [{
                label: '确认覆盖',
                className: 'danger',
                action: () => syncDirection(selectedStatus.game.id, direction, false),
            }],
        });
    }

    async function syncDirection(id: string, direction: 'cloud-to-local' | 'local-to-cloud', launchAfter: boolean) {
        const gameName = selectedStatus?.game.name ?? id;
        setTransferProgress({
            gameId: id,
            gameName,
            direction,
            phase: direction === 'local-to-cloud' ? 'upload' : 'download',
            message: direction === 'local-to-cloud' ? '准备上传本地存档' : '准备下载云端存档',
            currentBytes: 0,
            totalBytes: 0,
            currentPath: '',
            done: false,
        });
        await run(() => SyncGame(id, direction), async (result) => {
            const state = await GetAppState();
            setAppState(state);
            const refreshed = state.games.find((item) => item.game.id === id);
            if (refreshed) {
                chooseGame(refreshed);
            }
            if (launchAfter) {
                await LaunchGame(id);
            }
            const launchText = launchAfter ? '，并已发送启动命令' : '';
            setNotice(result.backupPath ? `覆盖完成，原目录已备份到 ${result.backupPath}${launchText}` : `覆盖完成${launchText}`);
            appendLog(direction === 'cloud-to-local' ? '已下载云端到本地' : '已上传本地到云端');
        });
        window.setTimeout(() => setTransferProgress(null), 1400);
    }

    function requestDelete() {
        if (!selectedStatus) {
            return;
        }
        setConfirm({
            title: '删除游戏配置',
            body: `确认删除 ${selectedStatus.game.name} 的配置？\n这只会删除配置记录，不会删除本地存档目录，也不会删除云端 data 里的存档。`,
            actions: [{
                label: '确认删除',
                className: 'danger',
                action: async () => {
                    await run(() => DeleteGame(selectedStatus.game.id), (state) => {
                        setAppState(state);
                        setSelectedId('');
                        setForm({...emptyGame});
                        setConfigOpen(false);
                        setNotice('配置已删除');
                        appendLog(`已删除 ${selectedStatus.game.name} 配置`);
                    });
                },
            }],
        });
    }

    async function launchGame() {
        if (!selectedStatus) {
            return;
        }
        if (selectedStatus.state !== 'in-sync') {
            setConfirm({
                title: '启动前确认存档',
                body: `当前本地与云端不一致。游戏启动后会读取本地存档目录：${selectedStatus.game.localSavePath}。${freshnessText(selectedStatus)}。`,
                actions: [
                    {
                        label: '云端覆盖本地后启动',
                        className: 'danger',
                        action: () => syncDirection(selectedStatus.game.id, 'cloud-to-local', true),
                    },
                    {
                        label: '继续使用本地启动',
                        className: 'primary',
                        action: () => run(() => LaunchGame(selectedStatus.game.id), () => {
                            setNotice('已使用当前本地存档启动游戏');
                            appendLog('已启动游戏');
                        }),
                    },
                ],
            });
            return;
        }
        await run(() => LaunchGame(selectedStatus.game.id), () => {
            setNotice('游戏启动命令已发送');
            appendLog('已启动游戏');
        });
    }

    async function testCloudService() {
        await run(() => TestCloudServerURL(cloudUrl, cloudPassword), (message) => {
            setNotice(message || '云服务连接正常');
            appendLog('云服务测试通过');
        });
    }

    async function saveCloudService(mode?: 'offline') {
        const nextUrl = mode === 'offline' ? 'offline' : cloudUrl;
        await run(() => SaveCloudServerURL(nextUrl, cloudPassword), (state) => {
            setAppState(state);
            setCloudUrl(state.cloudServerURL || '');
            setCloudConfigOpen(false);
            setNotice(mode === 'offline' ? '已切换为离线使用，只能本地备份。' : '云地址已保存');
            appendLog(mode === 'offline' ? '已切换离线使用' : '已保存云服务地址');
        });
    }

    async function changeCloudPassword() {
        if (!newCloudPassword) {
            return;
        }
        setConfirm({
            title: '修改服务端密码',
            body: '确认修改服务端连接密码？修改后其他客户端需要使用新密码重新连接。',
            actions: [{
                label: '确认修改',
                className: 'danger',
                action: async () => {
                    await run(() => ChangeCloudPassword(newCloudPassword), (state) => {
                        setAppState(state);
                        setCloudPassword(newCloudPassword);
                        setNewCloudPassword('');
                        setPasswordChangeOpen(false);
                        setNotice('服务端密码已修改');
                        appendLog('已修改服务端密码');
                    });
                },
            }],
        });
    }

    async function compareSelectedGame() {
        if (!selectedStatus) {
            return;
        }
        warnIfCloudUnavailable();
        await run(() => CompareGame(selectedStatus.game.id), (result) => {
            setCompareResult(result);
            const total = result.status.localOnly + result.status.cloudOnly + result.status.changed;
            setNotice(total === 0 ? '本地和云端一致' : '对比完成');
            appendLog(`已对比 ${selectedStatus.game.name}`);
        });
    }

    function warnIfCloudUnavailable() {
        if (!appState || appState.cloudStatus === 'running') {
            return;
        }
        setNotice(appState.cloudStatus === 'offline' ? '当前是离线使用模式，云端同步不可用。' : '云端可能离线或未设置，上传、下载、对比可能不可用。');
    }

    async function confirmAction(action: () => Promise<void>) {
        setConfirm(null);
        await action();
    }

    function showContextMenu(event: MouseEvent, status: main.GameStatus) {
        event.preventDefault();
        chooseGame(status);
        setContextMenu({x: event.clientX, y: event.clientY, status});
    }

    async function openGamePath(target: 'local' | 'cloud' | 'game') {
        if (!selectedStatus) {
            return;
        }
        setContextMenu(null);
        await run(() => OpenGamePath(selectedStatus.game.id, target), () => appendLog(`已打开${pathTargetName(target)}`));
    }

    async function exportGameConfig(status: main.GameStatus) {
        setContextMenu(null);
        await run(() => ExportGameConfig(status.game.id), (path) => {
            if (path) {
                setNotice(`已导出配置：${path}`);
                appendLog(`已导出 ${status.game.name} 配置`);
            }
        });
    }

    async function importGameConfig() {
        await run(ImportGameConfig, (game) => {
            setSelectedId('');
            setForm({
                ...emptyGame,
                ...game,
                autoUploadMode: game.autoUploadMode || 'manual',
                autoUploadIntervalMinutes: game.autoUploadIntervalMinutes || 5,
                saveSubdir: '',
            });
            setConfigOpen(true);
            setNotice('已导入配置，请确认路径后保存');
            appendLog(`已导入 ${game.name || game.folderName} 配置`);
        });
    }

    async function pickGameExe() {
        await run(PickGameExe, (path) => {
            if (path) {
                setForm((current) => ({...current, gameExePath: path}));
            }
        });
    }

    async function pickSaveDirectory() {
        await run(PickSaveDirectory, (path) => {
            if (path) {
                setForm((current) => ({...current, localSavePath: path}));
            }
        });
    }

    function requestBackupAction(mode: BackupMode) {
        if (!selectedStatus) {
            return;
        }
        setContextMenu(null);
        if (mode === 'cloud') {
            void openBackups('cloud');
            return;
        }
        setConfirm({
            title: '本地备份',
            body: '备份当前本地存档，或从已有本地备份还原到本地游戏存档目录。',
            actions: [
                {
                    label: '备份',
                    className: 'primary',
                    action: createBackup,
                },
                {
                    label: '还原',
                    className: 'primary',
                    action: () => openBackups('local'),
                },
            ],
        });
    }

    async function createBackup() {
        if (!selectedStatus) {
            return;
        }
        await run(() => CreateManualBackup(selectedStatus.game.id), (backup) => {
            setNotice(`本地备份完成：${backup.name}`);
            appendLog(`已创建 ${selectedStatus.game.name} 本地备份`);
        });
    }

    async function openBackups(mode: BackupMode) {
        if (!selectedStatus) {
            return;
        }
        setContextMenu(null);
        setBackupMode(mode);
        const task = mode === 'local' ? ListBackups : ListCloudBackups;
        await run(() => task(selectedStatus.game.id), (items) => {
            setBackups(items);
            setBackupOpen(true);
        });
    }

    function requestRestoreBackup(backup: main.BackupInfo) {
        if (!selectedStatus) {
            return;
        }
        setBackupOpen(false);
        setConfirm({
            title: backupMode === 'local' ? '还原本地备份' : '还原云端备份',
            body: backupMode === 'local'
                ? `确认还原这个本地备份？\n备份：${backup.name}\n时间：${formatDateTime(backup.createdAt)}\n\n还原只会写入本地游戏存档目录：${selectedStatus.game.localSavePath}\n不会直接写入云端。需要同步到云端时，请还原完成后再点“上传本地”。`
                : `确认还原这个云端备份？\n备份：${backup.name}\n时间：${formatDateTime(backup.createdAt)}\n\n还原会写入云端存档，不会直接写入本地游戏目录。`,
            actions: [{
                label: '确认还原',
                className: 'danger',
                action: async () => {
                    const task = backupMode === 'local' ? RestoreBackup : RestoreCloudBackup;
                    await run(() => task(selectedStatus.game.id, backup.name), async (result) => {
                        const state = await GetAppState();
                        setAppState(state);
                        const refreshed = state.games.find((item) => item.game.id === selectedStatus.game.id);
                        if (refreshed) {
                            chooseGame(refreshed);
                        }
                        setNotice(result.backupPath ? `已还原备份，原存档已备份到 ${result.backupPath}` : '已还原备份');
                        appendLog(`已还原${backupMode === 'local' ? '本地' : '云端'}备份 ${backup.name}`);
                    });
                },
            }],
        });
    }

    const games = appState?.games ?? [];

    return (
        <main className="shell" onClick={() => setContextMenu(null)}>
            <aside className="sidebar">
                <div className="brand">
                    <Gamepad2 size={26}/>
                    <div>
                        <h1>hebe游戏存档同步</h1>
                    </div>
                </div>

                <button className="primary full" onClick={createNewGame} disabled={busy} title="新增游戏">
                    <Plus size={18}/>
                    新增游戏
                </button>

                <div className="game-list">
                    {games.map((status) => (
                        <button
                            key={status.game.id}
                            className={`game-row ${selectedId === status.game.id ? 'active' : ''}`}
                            onClick={() => chooseGame(status)}
                            onContextMenu={(event) => showContextMenu(event, status)}
                        >
                            {status.iconData ? (
                                <span className="game-icon-wrap">
                                    <img className="game-icon" src={status.iconData} alt=""/>
                                    <span className={`dot mini ${status.state}`}/>
                                </span>
                            ) : (
                                <span className={`dot ${status.state}`}/>
                            )}
                            <span>
                                <strong>{status.game.name}</strong>
                                <small>{status.game.folderName}</small>
                            </span>
                        </button>
                    ))}
                    {games.length === 0 && <div className="empty">暂无游戏配置</div>}
                </div>

                <div className="cloud-service">
                    <div className="cloud-service-head">
                        <span className={`status-pill ${appState?.cloudStatus ?? 'stopped'}`}>
                            {cloudStatusLabel(appState?.cloudStatus)}
                        </span>
                        <button className="ghost compact icon-only" onClick={() => setCloudConfigOpen(true)} disabled={busy} title="云端配置">
                            <Settings size={15}/>
                        </button>
                    </div>
                    <details className="cloud-details">
                        <summary>{cloudSummary.synced} 已同步 / {cloudSummary.attention} 需处理</summary>
                        <div className="cloud-facts">
                            <span>地址</span>
                            <strong>{appState?.cloudServerURL ?? '未设置'}</strong>
                            <span>状态</span>
                            <strong>{appState?.cloudMessage ?? '正在读取状态'}</strong>
                        </div>
                    </details>
                </div>
            </aside>

            <section className="workspace">
                {(notice || error) && (
                    <div className={`banner ${error ? 'error' : 'success'}`}>
                        {error || notice}
                    </div>
                )}

                <div className="status-area">
                    <section className="panel status-panel">
                        <div className="panel-title">
                            <div>
                                <h2>存档状态</h2>
                                <span>{selectedStatus?.message ?? '选择一个游戏查看差异'}</span>
                            </div>
                            {selectedStatus && (
                                <div className="title-actions">
                                    <button className="ghost compact" onClick={editSelectedGame} disabled={busy} title="编辑游戏配置">
                                        <Pencil size={16}/>
                                        编辑
                                    </button>
                                </div>
                            )}
                        </div>

                        {selectedStatus ? (
                            <>
                                <div className={`sync-state ${selectedStatus.state}`}>
                                    <span>{stateLabels[selectedStatus.state] ?? selectedStatus.state}</span>
                                    <strong>{selectedStatus.localOnly + selectedStatus.cloudOnly + selectedStatus.changed}</strong>
                                </div>

                                <div className="status-brief">
                                    <span>本地 {selectedStatus.localFiles}</span>
                                    <span>云端 {selectedStatus.cloudFiles}</span>
                                    <span>{freshnessLabel(selectedStatus)}</span>
                                </div>

                                <div className="latest-summary">
                                    <div className={latestFreshClass(selectedStatus, 'local')}>
                                        <span>本地最新文件</span>
                                        {latestFreshClass(selectedStatus, 'local') && <em>较新</em>}
                                        <strong>{formatDateTime(selectedStatus.localModified)}</strong>
                                        <p>{selectedStatus.localLatestPath || '无文件'}</p>
                                    </div>
                                    <div className={latestFreshClass(selectedStatus, 'cloud')}>
                                        <span>云端最新文件</span>
                                        {latestFreshClass(selectedStatus, 'cloud') && <em>较新</em>}
                                        <strong>{formatDateTime(selectedStatus.cloudModified)}</strong>
                                        <p>{selectedStatus.cloudLatestPath || '无文件'}</p>
                                    </div>
                                </div>

                                <div className="actions">
                                    <button className="launch-button" onClick={launchGame} disabled={busy || !selectedStatus.game.gameExePath} title="启动游戏">
                                        <Play size={17}/>
                                        启动游戏
                                    </button>
                                    {!isOfflineMode && (
                                        <>
                                            <button className="ghost" onClick={compareSelectedGame} disabled={busy} title="快速对比本地和云端差异">
                                                <RefreshCw size={17}/>
                                                快速对比
                                            </button>
                                            <button className={`direction-action download ${directionTone(selectedStatus, 'cloud-to-local')}`} onClick={() => requestSync('cloud-to-local')} disabled={busy} title="下载云端到本地">
                                                <CloudDownload size={17}/>
                                                下载云端
                                            </button>
                                            <button className={`direction-action upload ${directionTone(selectedStatus, 'local-to-cloud')}`} onClick={() => requestSync('local-to-cloud')} disabled={busy} title="上传本地到云端">
                                                <CloudUpload size={17}/>
                                                上传本地
                                            </button>
                                        </>
                                    )}
                                    <button className="ghost" onClick={() => requestBackupAction('local')} disabled={busy} title="本地备份">
                                        <Archive size={17}/>
                                        本地备份
                                    </button>
                                    {!isOfflineMode && (
                                        <button className="ghost" onClick={() => requestBackupAction('cloud')} disabled={busy} title="云端备份">
                                            <History size={17}/>
                                            云端备份
                                        </button>
                                    )}
                                </div>

                                {compareResult && (
                                    <section className="compare-panel">
                                        <div className="compare-head">
                                            <strong>对比结果</strong>
                                            <span>{formatDateTime(compareResult.checkedAt)}{compareResult.truncated ? ' · 已截断显示' : ''}</span>
                                        </div>
                                        <CompareGroup title="本地新增" items={compareResult.localOnly}/>
                                        <CompareGroup title="云端新增" items={compareResult.cloudOnly}/>
                                        <CompareGroup title="内容不同" items={compareResult.changed}/>
                                    </section>
                                )}

                                <section className="log-panel">
                                    <span>最新日志</span>
                                    {activityLog.map((item) => <p key={item}>{item}</p>)}
                                </section>

                                <details className="details-block">
                                    <summary>更多详情</summary>
                                    <div className="path-block">
                                        <span>差异</span>
                                        <p>本地新增 {selectedStatus.localOnly} · 云端新增 {selectedStatus.cloudOnly} · 内容变化 {selectedStatus.changed}</p>
                                        <span>判断依据</span>
                                        <p>{selectedStatus.lastChangeReason || '内容一致'}{selectedStatus.lastChangePath ? `：${selectedStatus.lastChangePath}` : ''}</p>
                                        <span>本地</span>
                                        <p>{selectedStatus.game.localSavePath}</p>
                                        <span>云端</span>
                                        <p>{selectedStatus.cloudPath}</p>
                                        <span>数据目录</span>
                                        <p>{appState?.dataDir ?? '未读取'}</p>
                                        <span>游戏目录</span>
                                        <p>{selectedStatus.game.gameExePath ? selectedStatus.game.gameExePath : '未设置'}</p>
                                    </div>
                                </details>
                            </>
                        ) : (
                            <div className="empty large">选择或新增一个游戏</div>
                        )}
                    </section>
                </div>
            </section>

            {contextMenu && (
                <div className="context-menu" style={{left: contextMenu.x, top: contextMenu.y}} onClick={(event) => event.stopPropagation()}>
                    <button onClick={editSelectedGame}><Pencil size={15}/> 编辑配置</button>
                    <button onClick={() => openGamePath('local')}><FolderOpen size={15}/> 打开本地存档</button>
                    <button onClick={() => openGamePath('cloud')}><FolderOpen size={15}/> 打开云端文件夹</button>
                    <button onClick={() => openGamePath('game')} disabled={!contextMenu.status.game.gameExePath}><FolderOpen size={15}/> 打开游戏目录</button>
                    <button onClick={() => exportGameConfig(contextMenu.status)}><FileDown size={15}/> 导出游戏配置</button>
                    <hr/>
                    <button onClick={() => requestBackupAction('local')}><Archive size={15}/> 本地备份</button>
                    {!isOfflineMode && <button onClick={() => requestBackupAction('cloud')}><RestoreIcon size={15}/> 云端备份</button>}
                    <hr/>
                    <button onClick={() => refresh()}><RefreshCw size={15}/> 刷新状态</button>
                    <button onClick={launchGame} disabled={!contextMenu.status.game.gameExePath}><Play size={15}/> 启动游戏</button>
                </div>
            )}

            {backupOpen && (
                <div className="modal-backdrop">
                    <div className="modal backup-modal">
                        <h3>{backupMode === 'local' ? '本地备份' : '云端备份'}</h3>
                        <p>{backupMode === 'local' ? '选择一个本地备份还原到本地游戏存档目录。不会直接写入云端。' : '选择一个云端备份还原到云端存档。不会直接写入本地游戏目录。'}</p>
                        <div className="backup-list">
                            {backups.map((backup) => (
                                <button key={backup.name} onClick={() => requestRestoreBackup(backup)}>
                                    <strong>{formatDateTime(backup.createdAt)}</strong>
                                    <span>{backup.files} 个文件 · 最新：{backup.latestPath || '无'}</span>
                                </button>
                            ))}
                            {backups.length === 0 && <div className="empty backup-empty">暂无备份</div>}
                        </div>
                        <div className="modal-actions">
                            <button className="ghost" onClick={() => setBackupOpen(false)}>取消</button>
                        </div>
                    </div>
                </div>
            )}

            {confirm && (
                <div className="modal-backdrop">
                    <div className="modal">
                        <h3>{confirm.title}</h3>
                        <p>{confirm.body}</p>
                        <div className="modal-actions">
                            <button className="ghost" onClick={() => setConfirm(null)} disabled={busy}>取消</button>
                            {confirm.actions.map((item) => (
                                <button key={item.label} className={item.className} onClick={() => confirmAction(item.action)} disabled={busy}>
                                    {item.label}
                                </button>
                            ))}
                        </div>
                    </div>
                </div>
            )}

            {transferProgress && (
                <div className="transfer-toast">
                    <div className="transfer-card">
                        <div className="transfer-head">
                            <strong>{transferProgress.gameName || '存档传输'}</strong>
                            <span>{transferProgress.message || '正在处理'}</span>
                        </div>
                        <div className="progress-track">
                            <div className="progress-fill" style={{width: `${transferPercent(transferProgress)}%`}}/>
                        </div>
                        <div className="transfer-foot">
                            <span>{transferProgress.currentPath || (transferProgress.done ? '完成' : '准备中')}</span>
                            <strong>{transferPercent(transferProgress)}%</strong>
                        </div>
                    </div>
                </div>
            )}

            {cloudConfigOpen && (
                <div className="modal-backdrop">
                    <div className="modal cloud-config-modal">
                        <div className="modal-head">
                            <div>
                                <h3>云端配置</h3>
                                <p>设置云服务地址、连接密码，并可修改服务端密码。</p>
                            </div>
                        </div>
                        <div className="form-grid">
                            <label>
                                云地址
                                <input value={cloudUrl} onChange={(event) => setCloudUrl(event.target.value)} placeholder="NAS-IP:27843 或 https://save.example.com"/>
                                <small>例：192.168.10.48:27843，或反代地址 https://save.example.com；留空表示未设置。</small>
                            </label>
                            <label>
                                连接密码
                                <input type="password" value={cloudPassword} onChange={(event) => setCloudPassword(event.target.value)} placeholder="默认密码 hebesave"/>
                                <small>默认密码：hebesave</small>
                            </label>
                            <div className="inline-actions">
                                <button className="ghost" onClick={testCloudService} disabled={busy} type="button">
                                    <RefreshCw size={15}/>
                                    测试连接
                                </button>
                            </div>
                            {passwordChangeOpen && (
                                <label>
                                    新服务端密码
                                    <input type="password" value={newCloudPassword} onChange={(event) => setNewCloudPassword(event.target.value)} placeholder="例：my-new-save-password"/>
                                    <small>会修改服务端配置里的密码，提交前会再次确认。</small>
                                </label>
                            )}
                        </div>
                        <div className="modal-actions">
                            <button className="ghost" onClick={() => setCloudConfigOpen(false)} disabled={busy}>关闭</button>
                            <button className="ghost" onClick={() => saveCloudService('offline')} disabled={busy}>
                                离线使用
                            </button>
                            <button className="ghost" onClick={() => passwordChangeOpen ? changeCloudPassword() : setPasswordChangeOpen(true)} disabled={busy || (passwordChangeOpen && !newCloudPassword)}>
                                <KeyRound size={15}/>
                                修改密码
                            </button>
                            <button className="primary" onClick={() => saveCloudService()} disabled={busy}>
                                <Save size={15}/>
                                保存
                            </button>
                        </div>
                    </div>
                </div>
            )}

            {configOpen && (
                <div className="modal-backdrop">
                    <form className="modal config-modal" onSubmit={submitGame}>
                        <div className="modal-head">
                            <div>
                                <h3>{selectedId ? '编辑游戏' : '新增游戏'}</h3>
                                <p>配置本地存档目录、游戏标识和启动目标。</p>
                            </div>
                            <button className="ghost compact" type="button" onClick={importGameConfig} disabled={busy}>
                                <FileUp size={15}/>
                                导入 JSON
                            </button>
                        </div>

                        <div className="form-grid">
                            <label>
                                游戏名
                                <input value={form.name} onChange={(event) => setForm({...form, name: event.target.value})}/>
                            </label>
                            <label>
                                游戏标识名
                                <input value={form.folderName} onChange={(event) => setForm({...form, folderName: event.target.value})} placeholder="默认使用游戏名，如 bg3"/>
                                <small>用于云端同步识别，只能用中文、字母、数字、-、_。</small>
                            </label>
                            <label>
                                本机存档路径
                                <div className="path-input-row">
                                    <input value={form.localSavePath} onChange={(event) => setForm({...form, localSavePath: event.target.value})}/>
                                    <button className="ghost compact icon-only" type="button" onClick={pickSaveDirectory} disabled={busy} title="选择存档文件夹">
                                        <FolderOpen size={16}/>
                                    </button>
                                </div>
                            </label>
                            <label>
                                启动目标
                                <div className="path-input-row">
                                    <input value={form.gameExePath} onChange={(event) => setForm({...form, gameExePath: event.target.value})} placeholder="D:\\Games\\Game\\game.exe 或 steam://rungameid/1086940"/>
                                    <button className="ghost compact icon-only" type="button" onClick={pickGameExe} disabled={busy} title="选择游戏程序">
                                        <FolderOpen size={16}/>
                                    </button>
                                </div>
                            </label>
                            <label>
                                自动上传
                                <select value={form.autoUploadMode || 'manual'} onChange={(event) => setForm({...form, autoUploadMode: event.target.value})}>
                                    <option value="manual">关闭自动上传，完全手动</option>
                                    <option value="ask-on-exit">游戏关闭后询问上传</option>
                                    <option value="on-exit">游戏关闭后自动上传</option>
                                    <option value="interval">运行中定时上传</option>
                                </select>
                            </label>
                            <details className="advanced-settings">
                                <summary>高级设置</summary>
                                <label>
                                    启动参数
                                    <input value={form.gameArgs || ''} onChange={(event) => setForm({...form, gameArgs: event.target.value})} placeholder="-windowed -noborder"/>
                                </label>
                                {form.autoUploadMode === 'interval' && (
                                    <label>
                                        上传间隔（分钟）
                                        <input type="number" min="1" value={form.autoUploadIntervalMinutes || 5} onChange={(event) => setForm({...form, autoUploadIntervalMinutes: Number(event.target.value)})}/>
                                    </label>
                                )}
                            </details>
                        </div>

                        <div className="modal-actions">
                            {selectedId && (
                                <button className="ghost danger-text" type="button" onClick={requestDelete} disabled={busy}>
                                    <Trash2 size={15}/>
                                    删除配置
                                </button>
                            )}
                            <button className="ghost" type="button" onClick={() => setConfigOpen(false)} disabled={busy}>取消</button>
                            <button className="primary" type="submit" disabled={busy}>保存配置</button>
                        </div>
                    </form>
                </div>
            )}
        </main>
    );
}

function formatDateTime(value: string) {
    if (!value) {
        return '无';
    }
    return new Date(value).toLocaleString();
}

function CompareGroup({title, items}: { title: string; items: main.CompareEntry[] }) {
    return (
        <div className="compare-group">
            <span>{title} · {items.length}</span>
            {items.length === 0 ? (
                <p>无</p>
            ) : items.map((item) => (
                <p key={`${title}-${item.path}`}>
                    <strong>{item.path}</strong>
                    <em>{compareSideLabel(item.newerSide)}</em>
                </p>
            ))}
        </div>
    );
}

function compareSideLabel(side: string) {
    if (side === 'local') {
        return '本地较新';
    }
    if (side === 'cloud') {
        return '云端较新';
    }
    return '时间接近';
}

function transferPercent(progress: TransferProgress) {
    if (progress.done) {
        return 100;
    }
    if (!progress.totalBytes || progress.totalBytes <= 0) {
        return progress.currentBytes > 0 ? 12 : 4;
    }
    return Math.max(4, Math.min(99, Math.round((progress.currentBytes / progress.totalBytes) * 100)));
}

function cloudStatusLabel(status?: string) {
    if (status === 'running') {
        return '云端已连接';
    }
    if (status === 'offline') {
        return '离线使用';
    }
    if (status === 'unset') {
        return '云端未设置';
    }
    return '云端离线';
}

function directionTone(status: main.GameStatus, direction: 'cloud-to-local' | 'local-to-cloud') {
    if (status.state === 'in-sync') {
        return 'synced';
    }
    if (status.lastChangeSide === 'cloud') {
        return direction === 'cloud-to-local' ? 'recommended' : 'secondary';
    }
    if (status.lastChangeSide === 'local') {
        return direction === 'local-to-cloud' ? 'recommended' : 'secondary';
    }
    return 'caution';
}

function latestFreshClass(status: main.GameStatus, side: 'local' | 'cloud') {
    if (status.lastChangeSide === side) {
        return 'is-fresh';
    }
    if (status.lastChangeSide === 'both') {
        return 'has-difference';
    }
    const localTime = Date.parse(status.localModified || '');
    const cloudTime = Date.parse(status.cloudModified || '');
    if (Number.isNaN(localTime) || Number.isNaN(cloudTime) || localTime === cloudTime) {
        return '';
    }
    return (side === 'local' ? localTime > cloudTime : cloudTime > localTime) ? 'is-fresh' : '';
}

function overwriteBody(status: main.GameStatus, direction: 'cloud-to-local' | 'local-to-cloud') {
    const source = direction === 'cloud-to-local' ? '云端' : '本地';
    const target = direction === 'cloud-to-local' ? '本地' : '云端';
    const sourcePath = direction === 'cloud-to-local' ? status.cloudPath : status.game.localSavePath;
    const targetPath = direction === 'cloud-to-local' ? status.game.localSavePath : status.cloudPath;
    const warning = freshnessText(status);

    return [
        `判断：${warning}。`,
        `操作：使用${source}覆盖${target}。`,
        `来源：${sourcePath}`,
        `目标：${targetPath}`,
        `覆盖前会完整备份并校验目标目录；覆盖后也会校验来源和目标内容一致。`,
    ].join('\n');
}

function freshnessText(status: main.GameStatus) {
    const side = freshnessLabel(status);
    const reason = status.lastChangeReason || status.message;
    const path = status.lastChangePath ? `，依据文件：${status.lastChangePath}` : '';
    return `${side}，依据：${reason}${path}`;
}

function freshnessLabel(status: main.GameStatus) {
    const totalDiff = status.localOnly + status.cloudOnly + status.changed;
    if (status.state === 'in-sync' || totalDiff === 0 || !status.lastChangeSide) {
        return '内容一致';
    }
    return sideLabels[status.lastChangeSide] ?? '需要人工确认';
}

function pathTargetName(target: 'local' | 'cloud' | 'game') {
    return target === 'local' ? '本地存档' : target === 'cloud' ? '云端文件夹' : '游戏目录';
}

export default App;
