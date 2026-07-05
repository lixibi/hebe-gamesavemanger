export namespace main {
	
	export class GameConfig {
	    id: string;
	    name: string;
	    folderName: string;
	    localSavePath: string;
	    gameExePath: string;
	    gameArgs: string;
	    autoUploadMode: string;
	    autoUploadIntervalMinutes: number;
	    saveSubdir?: string;
	
	    static createFrom(source: any = {}) {
	        return new GameConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.folderName = source["folderName"];
	        this.localSavePath = source["localSavePath"];
	        this.gameExePath = source["gameExePath"];
	        this.gameArgs = source["gameArgs"];
	        this.autoUploadMode = source["autoUploadMode"];
	        this.autoUploadIntervalMinutes = source["autoUploadIntervalMinutes"];
	        this.saveSubdir = source["saveSubdir"];
	    }
	}
	export class GameStatus {
	    game: GameConfig;
	    iconData: string;
	    cloudPath: string;
	    state: string;
	    message: string;
	    lastChangeSide: string;
	    lastChangeReason: string;
	    lastChangePath: string;
	    lastCheckedAt: string;
	    localFiles: number;
	    cloudFiles: number;
	    localBytes: number;
	    cloudBytes: number;
	    localOnly: number;
	    cloudOnly: number;
	    changed: number;
	    localModified: string;
	    cloudModified: string;
	    localLatestPath: string;
	    cloudLatestPath: string;
	
	    static createFrom(source: any = {}) {
	        return new GameStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.game = this.convertValues(source["game"], GameConfig);
	        this.iconData = source["iconData"];
	        this.cloudPath = source["cloudPath"];
	        this.state = source["state"];
	        this.message = source["message"];
	        this.lastChangeSide = source["lastChangeSide"];
	        this.lastChangeReason = source["lastChangeReason"];
	        this.lastChangePath = source["lastChangePath"];
	        this.lastCheckedAt = source["lastCheckedAt"];
	        this.localFiles = source["localFiles"];
	        this.cloudFiles = source["cloudFiles"];
	        this.localBytes = source["localBytes"];
	        this.cloudBytes = source["cloudBytes"];
	        this.localOnly = source["localOnly"];
	        this.cloudOnly = source["cloudOnly"];
	        this.changed = source["changed"];
	        this.localModified = source["localModified"];
	        this.cloudModified = source["cloudModified"];
	        this.localLatestPath = source["localLatestPath"];
	        this.cloudLatestPath = source["cloudLatestPath"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AppState {
	    rootDir: string;
	    configPath: string;
	    dataDir: string;
	    cloudServerURL: string;
	    cloudPassword: string;
	    cloudStatus: string;
	    cloudMessage: string;
	    cloudGameCount: number;
	    games: GameStatus[];
	
	    static createFrom(source: any = {}) {
	        return new AppState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rootDir = source["rootDir"];
	        this.configPath = source["configPath"];
	        this.dataDir = source["dataDir"];
	        this.cloudServerURL = source["cloudServerURL"];
	        this.cloudPassword = source["cloudPassword"];
	        this.cloudStatus = source["cloudStatus"];
	        this.cloudMessage = source["cloudMessage"];
	        this.cloudGameCount = source["cloudGameCount"];
	        this.games = this.convertValues(source["games"], GameStatus);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class BackupInfo {
	    name: string;
	    path: string;
	    createdAt: string;
	    files: number;
	    bytes: number;
	    latestModified: string;
	    latestPath: string;
	
	    static createFrom(source: any = {}) {
	        return new BackupInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.createdAt = source["createdAt"];
	        this.files = source["files"];
	        this.bytes = source["bytes"];
	        this.latestModified = source["latestModified"];
	        this.latestPath = source["latestPath"];
	    }
	}
	
	
	export class SyncResult {
	    backupPath: string;
	    status: GameStatus;
	
	    static createFrom(source: any = {}) {
	        return new SyncResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.backupPath = source["backupPath"];
	        this.status = this.convertValues(source["status"], GameStatus);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

