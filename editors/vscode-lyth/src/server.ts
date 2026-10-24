import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import * as vscode from 'vscode';
import {
    ExecuteCommandRequest,
    LanguageClient,
    LanguageClientOptions,
    ServerOptions,
    State,
    WorkDoneProgress,
    WorkDoneProgressBegin,
    WorkDoneProgressEnd,
    WorkDoneProgressReport,
} from 'vscode-languageclient/node';

const EXE = process.platform === 'win32' ? 'lyth.exe' : 'lyth';
const MODULE = 'github.com/lyth-lang/lyth/cmd/lyth';

/** Where `go install` puts commands: $GOBIN, else $GOPATH/bin, else ~/go/bin. */
function goBin(): string {
    if (process.env.GOBIN) {
        return process.env.GOBIN;
    }
    const gopath = (process.env.GOPATH ?? '').split(path.delimiter).find((p) => p) || path.join(os.homedir(), 'go');
    return path.join(gopath, 'bin');
}

/** Directories searched after the PATH: Go's bin directory and Homebrew's. */
function extraDirs(): string[] {
    return [goBin(), '/opt/homebrew/bin', '/usr/local/bin'];
}

function isFile(p: string): boolean {
    try {
        return fs.statSync(p).isFile();
    } catch {
        return false;
    }
}

function onPath(name: string): string | undefined {
    const dirs = (process.env.PATH ?? '').split(path.delimiter).filter((d) => d);
    return [...dirs, ...extraDirs()].map((d) => path.join(d, name)).find(isFile);
}

/** The `lyth` executable: the `lyth.path` setting, else `lyth` on the PATH, else Go's bin directory. */
export function findLyth(): string | undefined {
    let configured = vscode.workspace.getConfiguration('lyth').get<string>('path', '').trim();
    if (!configured) {
        return onPath(EXE);
    }
    if (!/[\\/]/.test(configured)) {
        return onPath(configured);
    }
    const folder = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    configured = configured.replace(/^~(?=$|[\\/])/, os.homedir());
    if (folder) {
        configured = configured.replace(/\$\{workspaceFolder\}/g, folder);
    }
    const resolved = path.isAbsolute(configured) || !folder ? configured : path.join(folder, configured);
    return isFile(resolved) ? resolved : undefined;
}

/** The environment for lyth processes. Go's bin directory and Homebrew's are added to the PATH when they are
 * missing (VS Code started outside a login shell), so the server and builds find `go`. */
export function lythEnv(): Record<string, string> {
    const env: Record<string, string> = {};
    for (const [key, value] of Object.entries(process.env)) {
        if (value !== undefined) {
            env[key] = value;
        }
    }
    const key = Object.keys(env).find((k) => k.toUpperCase() === 'PATH') ?? 'PATH';
    const dirs = (env[key] ?? '').split(path.delimiter).filter((d) => d);
    env[key] = [...dirs, ...extraDirs().filter((d) => !dirs.includes(d))].join(path.delimiter);
    return env;
}

/** Runs `lyth lsp` and connects it to VS Code. */
export class LythServer implements vscode.Disposable {
    private client: LanguageClient | undefined;
    private starting: Promise<void> = Promise.resolve();
    private readonly output = vscode.window.createOutputChannel('Lyth Language Server', { log: true });
    private readonly status = vscode.languages.createLanguageStatusItem('lyth.server', { language: 'lyth' });
    private endVet: (() => void) | undefined;

    constructor() {
        this.status.name = 'Lyth Language Server';
        this.status.text = 'lyth';
    }

    start(): Promise<void> {
        this.starting = this.starting.then(() => this.doStart()).catch((e) => this.output.appendLine(`Starting the server failed: ${e}`));
        return this.starting;
    }

    async restart(): Promise<void> {
        await this.stop();
        await this.start();
    }

    async stop(): Promise<void> {
        await this.starting;
        const client = this.client;
        this.client = undefined;
        this.vetEnded();
        if (client) {
            await client.dispose().catch(() => undefined);
        }
    }

    showOutput(): void {
        this.output.show();
    }

    /** The Go generated for a .lyth file, or undefined if the server isn't running. */
    async generatedGo(source: vscode.Uri): Promise<string | undefined> {
        await this.starting;
        const client = this.client;
        if (!client?.isRunning()) {
            return undefined;
        }
        const uri = client.code2ProtocolConverter.asUri(source);
        return client.sendRequest(ExecuteCommandRequest.type, { command: 'lyth.showGo', arguments: [uri] });
    }

    private async doStart(): Promise<void> {
        if (this.client) {
            return;
        }
        const command = findLyth();
        if (!command) {
            this.missing();
            return;
        }
        const serverOptions: ServerOptions = { command, args: ['lsp'], options: { env: lythEnv() } };
        const clientOptions: LanguageClientOptions = {
            documentSelector: [{ scheme: 'file', language: 'lyth' }],
            initializationOptions: { vet: vscode.workspace.getConfiguration('lyth').get('vet', true) },
            outputChannel: this.output,
            middleware: {
                // Snippets come from snippets/lyth.json (they work without the server), so the
                // server's copies would only show up twice.
                provideCompletionItem: async (document, position, context, token, next) => {
                    const result = await next(document, position, context, token);
                    const keep = (item: vscode.CompletionItem) => item.kind !== vscode.CompletionItemKind.Snippet;
                    if (Array.isArray(result)) {
                        return result.filter(keep);
                    }
                    if (result) {
                        result.items = result.items.filter(keep);
                    }
                    return result;
                },
            },
        };
        const client = new LanguageClient('lyth', 'Lyth Language Server', serverOptions, clientOptions);
        // `go vet` progress; the server reports it without creating the token first
        client.onProgress(WorkDoneProgress.type, 'lyth-vet', (value) => this.vetProgress(value));
        client.onDidChangeState((e) => {
            if (e.newState === State.Stopped) {
                this.vetEnded();
            }
        });
        this.client = client;
        try {
            await client.start();
        } catch (e) {
            this.client = undefined;
            this.status.severity = vscode.LanguageStatusSeverity.Error;
            this.status.text = 'lyth failed to start';
            this.status.detail = String(e);
            this.status.command = { title: 'Show Output', command: 'lyth.showOutput' };
            return;
        }
        const version = client.initializeResult?.serverInfo?.version;
        this.status.severity = vscode.LanguageStatusSeverity.Information;
        this.status.text = version ? `lyth ${version}` : 'lyth';
        this.status.detail = command;
        this.status.command = { title: 'Restart', command: 'lyth.restartServer' };
    }

    private missing(): void {
        const configured = vscode.workspace.getConfiguration('lyth').get<string>('path', '').trim();
        this.status.severity = vscode.LanguageStatusSeverity.Error;
        this.status.text = 'lyth not found';
        this.status.detail = configured ? `lyth.path: ${configured}` : `install it with go install ${MODULE}@latest`;
        this.status.command = { title: 'Retry', command: 'lyth.restartServer' };
        const message = configured
            ? `The lyth command set in lyth.path was not found: ${configured}`
            : `The Lyth language server needs the lyth command. Install it with "go install ${MODULE}@latest" (or "brew install lyth-lang/tap/lyth"), or set lyth.path.`;
        const install = 'Install with Go';
        const settings = 'Open Settings';
        void vscode.window.showErrorMessage(message, ...(configured ? [] : [install]), settings).then(async (choice) => {
            if (choice === install) {
                await this.install();
            } else if (choice === settings) {
                await vscode.commands.executeCommand('workbench.action.openSettings', 'lyth.path');
            }
        });
    }

    /** Runs `go install` of the lyth command in a terminal and starts the server when it succeeds. */
    private async install(): Promise<void> {
        const execution = new vscode.ProcessExecution('go', ['install', `${MODULE}@latest`], { env: lythEnv() });
        const task = new vscode.Task({ type: 'lyth', command: 'install' }, vscode.TaskScope.Workspace, `go install ${MODULE}@latest`, 'lyth', execution);
        const started = await vscode.tasks.executeTask(task);
        const listener = vscode.tasks.onDidEndTaskProcess((e) => {
            if (e.execution === started) {
                listener.dispose();
                if (e.exitCode === 0) {
                    void this.restart();
                }
            }
        });
    }

    private vetProgress(value: WorkDoneProgressBegin | WorkDoneProgressReport | WorkDoneProgressEnd): void {
        if (value.kind === 'begin') {
            this.vetEnded();
            this.status.busy = true;
            const done = new Promise<void>((resolve) => (this.endVet = resolve));
            void vscode.window.withProgress({ location: vscode.ProgressLocation.Window, title: value.title }, () => done);
        } else if (value.kind === 'end') {
            this.vetEnded();
        }
    }

    private vetEnded(): void {
        this.endVet?.();
        this.endVet = undefined;
        this.status.busy = false;
    }

    dispose(): void {
        void this.stop();
        this.status.dispose();
        this.output.dispose();
    }
}
