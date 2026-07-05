import {FormEvent, MouseEvent, useEffect, useMemo, useState} from 'react';
import {
    CloudDownload,
    CloudUpload,
    Archive,
    FileDown,
    FileUp,
    FolderOpen,
    History,
    Pencil,
    Gamepad2,
    Play,
    Plus,
    RefreshCw,
    RotateCcw,
    RotateCcw as RestoreIcon,
    Save,
    Trash2,
} from 'lucide-react';
import './App.css';
import {
    CreateManualBackup,
    DeleteGame,
    ExportGameConfig,
    GetAppState,
    ImportGameConfig,
    LaunchGame,
    ListBackups,
    OpenGamePath,
    PickGameExe,
    PickSaveDirectory,
    RestoreBackup,
    SaveGame,
    StartSyncthing,
    SyncGame
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
    unknown: '无法可靠判断新旧',
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
    const [backups, setBackups] = useState<main.BackupInfo[]>([]);
    const [activityLog, setActivityLog] = useState<string[]>(['等待操作']);

    const selectedStatus = useMemo(() => {
        return appState?.games?.find((item) => item.game.id === selectedId) ?? null;
    }, [appState, selectedId]);

    useEffect(() => {
        void refresh();
        const timer = window.setInterval(() => void refresh(false), 5000);
        return () => window.clearInterval(timer);
    }, [selectedId, configOpen]);

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
        const payload = {
            ...form,
            id: form.id || form.folderName,
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
            setNotice('配置已保存');
            appendLog(`已保存 ${payload.name} 配置`);
        });
    }

    function requestSync(direction: 'cloud-to-local' | 'local-to-cloud') {
        if (!selectedStatus) {
            return;
        }
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

    async function startSyncthing() {
        await run(StartSyncthing, (state) => {
            setAppState(state);
            setNotice('已尝试启动 Syncthing');
            appendLog('已尝试启动 Syncthing');
        });
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

    async function createManualBackup() {
        if (!selectedStatus) {
            return;
        }
        setContextMenu(null);
        await run(() => CreateManualBackup(selectedStatus.game.id), (backup) => {
            setNotice(`已备份当前存档：${backup.name}`);
            appendLog(`已备份 ${selectedStatus.game.name} 当前存档`);
        });
    }

    async function openRestoreBackups() {
        if (!selectedStatus) {
            return;
        }
        setContextMenu(null);
        await run(() => ListBackups(selectedStatus.game.id), (items) => {
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
            title: '还原备份',
            body: `确认还原这个备份？\n备份：${backup.name}\n时间：${formatDateTime(backup.createdAt)}\n\n还原只会写入本地游戏存档目录：${selectedStatus.game.localSavePath}\n不会直接写入云端。需要同步到云端时，请还原完成后再点“上传本地”。`,
            actions: [{
                label: '确认还原',
                className: 'danger',
                action: async () => {
                    await run(() => RestoreBackup(selectedStatus.game.id, backup.name), async (result) => {
                        const state = await GetAppState();
                        setAppState(state);
                        const refreshed = state.games.find((item) => item.game.id === selectedStatus.game.id);
                        if (refreshed) {
                            chooseGame(refreshed);
                        }
                        setNotice(result.backupPath ? `已还原备份，原存档已备份到 ${result.backupPath}` : '已还原备份');
                        appendLog(`已还原备份 ${backup.name}`);
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
                        <span>Wails + Syncthing</span>
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
                            <span className={`dot ${status.state}`}/>
                            <span>
                                <strong>{status.game.name}</strong>
                                <small>{status.game.folderName}</small>
                            </span>
                        </button>
                    ))}
                    {games.length === 0 && <div className="empty">暂无游戏配置</div>}
                </div>

                <div className="syncthing">
                    <span className={`status-pill ${appState?.syncthingStatus ?? 'stopped'}`}>
                        {appState?.syncthingStatus === 'running' ? 'Syncthing 运行中' : 'Syncthing 未运行'}
                    </span>
                    <p>{appState?.syncthingMessage ?? '正在读取状态'}</p>
                    <button className="ghost full" onClick={startSyncthing} disabled={busy} title="启动 Syncthing">
                        <RotateCcw size={16}/>
                        启动 Syncthing
                    </button>
                </div>
            </aside>

            <section className="workspace">
                <header className="topbar">
                    <div>
                        <p>数据目录</p>
                        <strong>{appState?.dataDir ?? '...'}</strong>
                    </div>
                    <button className="ghost" onClick={() => refresh()} disabled={busy} title="刷新状态">
                        <RefreshCw size={17}/>
                        刷新
                    </button>
                </header>

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
                                <button className="ghost compact" onClick={editSelectedGame} disabled={busy} title="编辑游戏配置">
                                    <Pencil size={16}/>
                                    编辑
                                </button>
                            )}
                        </div>

                        {selectedStatus ? (
                            <>
                                <div className={`sync-state ${selectedStatus.state}`}>
                                    <span>{stateLabels[selectedStatus.state] ?? selectedStatus.state}</span>
                                    <strong>{selectedStatus.localOnly + selectedStatus.cloudOnly + selectedStatus.changed}</strong>
                                </div>

                                <div className="metrics">
                                    <Metric label="本地文件" value={selectedStatus.localFiles}/>
                                    <Metric label="云端文件" value={selectedStatus.cloudFiles}/>
                                    <Metric label="本地新增" value={selectedStatus.localOnly}/>
                                    <Metric label="云端新增" value={selectedStatus.cloudOnly}/>
                                    <Metric label="内容变化" value={selectedStatus.changed}/>
                                    <Metric label="新旧判断" value={sideLabels[selectedStatus.lastChangeSide] ?? '无法判断'}/>
                                </div>

                                <div className="latest-summary">
                                    <div>
                                        <span>本地最新文件</span>
                                        <strong>{formatDateTime(selectedStatus.localModified)}</strong>
                                        <p>{selectedStatus.localLatestPath || '无文件'}</p>
                                    </div>
                                    <div>
                                        <span>云端最新文件</span>
                                        <strong>{formatDateTime(selectedStatus.cloudModified)}</strong>
                                        <p>{selectedStatus.cloudLatestPath || '无文件'}</p>
                                    </div>
                                </div>

                                <div className="actions">
                                    <button className={`direction-action download ${directionTone(selectedStatus, 'cloud-to-local')}`} onClick={() => requestSync('cloud-to-local')} disabled={busy} title="下载云端到本地">
                                        <CloudDownload size={17}/>
                                        下载云端
                                    </button>
                                    <button className={`direction-action upload ${directionTone(selectedStatus, 'local-to-cloud')}`} onClick={() => requestSync('local-to-cloud')} disabled={busy} title="上传本地到云端">
                                        <CloudUpload size={17}/>
                                        上传本地
                                    </button>
                                    <button className="launch-button" onClick={launchGame} disabled={busy || !selectedStatus.game.gameExePath} title="启动游戏">
                                        <Play size={17}/>
                                        启动游戏
                                    </button>
                                    <button className="ghost" onClick={createManualBackup} disabled={busy} title="备份当前存档">
                                        <Archive size={17}/>
                                        备份当前存档
                                    </button>
                                    <button className="ghost" onClick={openRestoreBackups} disabled={busy} title="查看最近备份">
                                        <History size={17}/>
                                        查看备份
                                    </button>
                                    <button className="ghost danger-text" onClick={requestDelete} disabled={busy} title="删除配置">
                                        <Trash2 size={17}/>
                                        删除配置
                                    </button>
                                </div>

                                <section className="log-panel">
                                    <span>最新日志</span>
                                    {activityLog.map((item) => <p key={item}>{item}</p>)}
                                </section>

                                <details className="details-block">
                                    <summary>路径与判断详情</summary>
                                    <div className="path-block">
                                        <span>判断依据</span>
                                        <p>{selectedStatus.lastChangeReason || '内容一致'}{selectedStatus.lastChangePath ? `：${selectedStatus.lastChangePath}` : ''}</p>
                                        <span>本地</span>
                                        <p>{selectedStatus.game.localSavePath}</p>
                                        <span>云端</span>
                                        <p>{selectedStatus.cloudPath}</p>
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
                    <button onClick={createManualBackup}><Archive size={15}/> 备份当前存档</button>
                    <button onClick={openRestoreBackups}><RestoreIcon size={15}/> 还原备份</button>
                    <hr/>
                    <button onClick={() => refresh()}><RefreshCw size={15}/> 刷新状态</button>
                    <button onClick={launchGame} disabled={!contextMenu.status.game.gameExePath}><Play size={15}/> 启动游戏</button>
                    <button className="danger-item" onClick={requestDelete}><Trash2 size={15}/> 删除配置</button>
                </div>
            )}

            {backupOpen && (
                <div className="modal-backdrop">
                    <div className="modal backup-modal">
                        <h3>还原备份</h3>
                        <p>选择一个备份还原到本地游戏存档目录。不会直接写入云端。</p>
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

            {configOpen && (
                <div className="modal-backdrop">
                    <form className="modal config-modal" onSubmit={submitGame}>
                        <div className="modal-head">
                            <div>
                                <h3>{selectedId ? '编辑游戏' : '新增游戏'}</h3>
                                <p>配置本地存档目录、云端游戏文件夹和游戏 exe 路径。</p>
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
                                云端文件夹名
                                <input value={form.folderName} onChange={(event) => setForm({...form, folderName: event.target.value})} placeholder="bg3"/>
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
                                游戏 exe 路径
                                <div className="path-input-row">
                                    <input value={form.gameExePath} onChange={(event) => setForm({...form, gameExePath: event.target.value})} placeholder="D:\\Games\\Game\\game.exe"/>
                                    <button className="ghost compact icon-only" type="button" onClick={pickGameExe} disabled={busy} title="选择游戏程序">
                                        <FolderOpen size={16}/>
                                    </button>
                                </div>
                            </label>
                            <label>
                                启动参数
                                <input value={form.gameArgs || ''} onChange={(event) => setForm({...form, gameArgs: event.target.value})} placeholder="-windowed -noborder"/>
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
                            {form.autoUploadMode === 'interval' && (
                                <label>
                                    上传间隔（分钟）
                                    <input type="number" min="1" value={form.autoUploadIntervalMinutes || 5} onChange={(event) => setForm({...form, autoUploadIntervalMinutes: Number(event.target.value)})}/>
                                </label>
                            )}
                        </div>

                        <div className="modal-actions">
                            <button className="ghost" type="button" onClick={() => setConfigOpen(false)} disabled={busy}>取消</button>
                            <button className="primary" type="submit" disabled={busy}>保存配置</button>
                        </div>
                    </form>
                </div>
            )}
        </main>
    );
}

function Metric({label, value}: { label: string; value: string | number }) {
    return (
        <div className="metric">
            <span>{label}</span>
            <strong>{value}</strong>
        </div>
    );
}

function formatDateTime(value: string) {
    if (!value) {
        return '无';
    }
    return new Date(value).toLocaleString();
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
    const side = sideLabels[status.lastChangeSide] ?? '无法可靠判断新旧';
    const reason = status.lastChangeReason || status.message;
    const path = status.lastChangePath ? `，依据文件：${status.lastChangePath}` : '';
    return `${side}，依据：${reason}${path}`;
}

function pathTargetName(target: 'local' | 'cloud' | 'game') {
    return target === 'local' ? '本地存档' : target === 'cloud' ? '云端文件夹' : '游戏目录';
}

export default App;
