export namespace main {
	
	export class StatusPayload {
	    status: string;
	    detail: string;
	    url: string;
	    localVersion: string;
	    dataDir: string;
	
	    static createFrom(source: any = {}) {
	        return new StatusPayload(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.detail = source["detail"];
	        this.url = source["url"];
	        this.localVersion = source["localVersion"];
	        this.dataDir = source["dataDir"];
	    }
	}

}

