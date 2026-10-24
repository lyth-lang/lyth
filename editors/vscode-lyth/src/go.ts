import * as path from 'node:path';
import * as vscode from 'vscode';
import type { LythServer } from './server';

const SCHEME = 'lyth-go';

/** `/dir/main.lyth` → `lyth-go:/dir/main.lyth.go`, a read-only document with the generated Go. */
export function generatedGoUri(source: vscode.Uri): vscode.Uri {
    return source.with({ scheme: SCHEME, path: `${source.path}.go` });
}

/** Shows the Go generated for .lyth files beside them, updated while the source is edited. */
export class GeneratedGo implements vscode.TextDocumentContentProvider, vscode.Disposable {
    private readonly changed = new vscode.EventEmitter<vscode.Uri>();
    readonly onDidChange = this.changed.event;
    private readonly timers = new Map<string, ReturnType<typeof setTimeout>>();
    private readonly subscriptions: vscode.Disposable[];

    constructor(private readonly server: LythServer) {
        this.subscriptions = [
            vscode.workspace.registerTextDocumentContentProvider(SCHEME, this),
            vscode.workspace.onDidChangeTextDocument((e) => this.sourceChanged(e.document)),
        ];
    }

    async provideTextDocumentContent(uri: vscode.Uri): Promise<string> {
        const source = uri.with({ scheme: 'file', path: uri.path.replace(/\.go$/, '') });
        const go = await this.server.generatedGo(source);
        if (go === undefined) {
            return '// The Lyth language server is not running.\n';
        }
        return go || `// No Go was generated for ${path.basename(source.fsPath)}; see the Problems panel.\n`;
    }

    async show(uri?: vscode.Uri): Promise<void> {
        const source = uri ?? vscode.window.activeTextEditor?.document.uri;
        if (!source || source.scheme !== 'file' || !source.path.endsWith('.lyth')) {
            void vscode.window.showInformationMessage('Open a Lyth file first.');
            return;
        }
        // The view's name ends in .go, so it shows as Go.
        const document = await vscode.workspace.openTextDocument(generatedGoUri(source));
        await vscode.window.showTextDocument(document, { viewColumn: vscode.ViewColumn.Beside, preview: true, preserveFocus: true });
    }

    /** Reloads every open view, for example after the server (re)started. */
    refreshAll(): void {
        for (const document of vscode.workspace.textDocuments) {
            if (document.uri.scheme === SCHEME) {
                this.changed.fire(document.uri);
            }
        }
    }

    private sourceChanged(document: vscode.TextDocument): void {
        if (document.languageId !== 'lyth') {
            return;
        }
        const view = generatedGoUri(document.uri);
        const key = view.toString();
        if (!vscode.workspace.textDocuments.some((d) => d.uri.toString() === key)) {
            return;
        }
        clearTimeout(this.timers.get(key));
        this.timers.set(key, setTimeout(() => {
            this.timers.delete(key);
            this.changed.fire(view);
        }, 300));
    }

    dispose(): void {
        this.timers.forEach(clearTimeout);
        this.subscriptions.forEach((s) => s.dispose());
        this.changed.dispose();
    }
}
