export namespace main {
	
	export class ScanRequest {
	    targets: string;
	    ports: string;
	    timeoutMs: number;
	    concurrency: number;
	    rounds: number;
	    intervalMs: number;
	
	    static createFrom(source: any = {}) {
	        return new ScanRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.targets = source["targets"];
	        this.ports = source["ports"];
	        this.timeoutMs = source["timeoutMs"];
	        this.concurrency = source["concurrency"];
	        this.rounds = source["rounds"];
	        this.intervalMs = source["intervalMs"];
	    }
	}
	export class ScanStarted {
	    targets: number;
	    warnings: string[];
	
	    static createFrom(source: any = {}) {
	        return new ScanStarted(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.targets = source["targets"];
	        this.warnings = source["warnings"];
	    }
	}

}

