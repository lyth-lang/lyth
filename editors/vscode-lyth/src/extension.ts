import * as vscode from 'vscode';
import { GeneratedGo } from './go';
import { LythTaskProvider, RunLenses, TASK_TYPE, runFile } from './run';
import { LythServer } from './server';

let server: LythServer | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
    const lyth = new LythServer();
    server = lyth;
    const go = new GeneratedGo(lyth);
    const lenses = new RunLenses();
    const restart = async () => {
        await lyth.restart();
        go.refreshAll();
    };
    context.subscriptions.push(
        lyth,
        go,
        lenses,
        vscode.languages.registerCodeLensProvider({ language: 'lyth', scheme: 'file' }, lenses),
        vscode.tasks.registerTaskProvider(TASK_TYPE, new LythTaskProvider()),
        vscode.commands.registerCommand('lyth.run', (uri?: vscode.Uri) => runFile(uri, 'run')),
        vscode.commands.registerCommand('lyth.test', (uri?: vscode.Uri) => runFile(uri, 'test')),
        vscode.commands.registerCommand('lyth.runTest', (uri: vscode.Uri, name: string) => runFile(uri, 'test', name)),
        vscode.commands.registerCommand('lyth.showGeneratedGo', (uri?: vscode.Uri) => go.show(uri)),
        vscode.commands.registerCommand('lyth.restartServer', restart),
        vscode.commands.registerCommand('lyth.showOutput', () => lyth.showOutput()),
        vscode.workspace.onDidChangeConfiguration((e) => {
            if (vscode.workspace.isTrusted && (e.affectsConfiguration('lyth.path') || e.affectsConfiguration('lyth.vet'))) {
                void restart();
            }
        }),
        vscode.workspace.onDidGrantWorkspaceTrust(async () => {
            lenses.refresh();
            await lyth.start();
            go.refreshAll();
        }),
    );
    // Without trust only the grammar and snippets work: the server runs the Go toolchain on the code.
    if (vscode.workspace.isTrusted) {
        await lyth.start();
        go.refreshAll();
    }
}

export function deactivate(): Promise<void> | undefined {
    return server?.stop();
}
