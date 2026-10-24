import * as fs from 'node:fs';
import * as path from 'node:path';
import * as vscode from 'vscode';
import { findLyth, lythEnv } from './server';

export const TASK_TYPE = 'lyth';

interface LythTaskDefinition extends vscode.TaskDefinition {
    command: string;
    args?: string[];
}

/** Whether `dir` or a folder up to `depth` levels below it holds a .lyth file. */
function hasLythFiles(dir: string, depth: number): boolean {
    let entries: fs.Dirent[];
    try {
        entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
        return false;
    }
    if (entries.some((e) => e.isFile() && e.name.endsWith('.lyth'))) {
        return true;
    }
    return depth > 0 && entries.some((e) => e.isDirectory() && !e.name.startsWith('.') && e.name !== 'node_modules' && e.name !== 'vendor' && hasLythFiles(path.join(dir, e.name), depth - 1));
}

/** A Go module with Lyth packages. */
function isLythModule(dir: string): boolean {
    return fs.existsSync(path.join(dir, 'go.mod')) && hasLythFiles(dir, 3);
}

function lythTask(definition: LythTaskDefinition, scope: vscode.WorkspaceFolder | vscode.TaskScope, name: string, cwd: string | undefined): vscode.Task {
    const args = [definition.command, ...(definition.args ?? [])];
    const execution = new vscode.ProcessExecution(findLyth() ?? 'lyth', args, { cwd, env: lythEnv() });
    const task = new vscode.Task(definition, scope, name, TASK_TYPE, execution);
    task.presentationOptions = { clear: true, showReuseMessage: false };
    return task;
}

/** `lyth run file.lyth`, or `lyth test` in the file's package (one test, with its name), for a file or the active editor's. */
export async function runFile(uri: vscode.Uri | undefined, command: 'run' | 'test', testName?: string): Promise<void> {
    const document = uri ? await vscode.workspace.openTextDocument(uri) : vscode.window.activeTextEditor?.document;
    if (!document || document.languageId !== 'lyth' || document.uri.scheme !== 'file') {
        void vscode.window.showInformationMessage('Open a Lyth file first.');
        return;
    }
    if (document.isDirty) {
        await document.save();
    }
    const file = document.uri.fsPath;
    const cwd = path.dirname(file);
    let args: string[];
    let label: string;
    if (command === 'run') {
        args = [file];
        label = `run ${path.basename(file)}`;
    } else if (testName === undefined) {
        args = ['-v', '.'];
        label = `test ${path.basename(cwd)}`;
    } else {
        args = ['-v', '-run', `^${testName}$`, '.'];
        label = `test ${testName}`;
    }
    const folder = vscode.workspace.getWorkspaceFolder(document.uri);
    if (!vscode.workspace.workspaceFolders?.length) {
        // tasks need a workspace: a single file opened on its own runs in a terminal
        const terminal = vscode.window.createTerminal({ name: `lyth ${label}`, cwd, env: lythEnv(), shellPath: findLyth() ?? 'lyth', shellArgs: [command, ...args] });
        terminal.show();
        return;
    }
    await vscode.tasks.executeTask(lythTask({ type: TASK_TYPE, command, args }, folder ?? vscode.TaskScope.Workspace, label, cwd));
}

/** `lyth build`, `run`, `test`, `check` and `vet` for each Lyth module in the workspace, and tasks.json entries of type `lyth`. */
export class LythTaskProvider implements vscode.TaskProvider {
    provideTasks(): vscode.Task[] {
        const tasks: vscode.Task[] = [];
        for (const folder of vscode.workspace.workspaceFolders ?? []) {
            if (!isLythModule(folder.uri.fsPath)) {
                continue;
            }
            for (const [command, args] of [['build', ['./...']], ['run', ['.']], ['test', ['./...']], ['check', ['./...']], ['vet', ['./...']]] as const) {
                const task = lythTask({ type: TASK_TYPE, command, args: [...args] }, folder, command, folder.uri.fsPath);
                if (command === 'build') {
                    task.group = vscode.TaskGroup.Build;
                } else if (command === 'test') {
                    task.group = vscode.TaskGroup.Test;
                }
                tasks.push(task);
            }
        }
        return tasks;
    }

    resolveTask(task: vscode.Task): vscode.Task | undefined {
        const definition = task.definition as LythTaskDefinition;
        if (typeof definition.command !== 'string') {
            return undefined;
        }
        const scope = task.scope ?? vscode.TaskScope.Workspace;
        const cwd = typeof scope === 'object' ? scope.uri.fsPath : vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
        return lythTask(definition, scope, task.name, cwd);
    }
}

// `func main(` at the start of a line
const MAIN = /^func\s+main\s*\(/;
// `func TestXxx(`, `func BenchmarkXxx(`, ... at the start of a line, in a _test.lyth file
const TEST = /^func\s+((?:Test|Benchmark|Fuzz|Example)\w*)\s*\(/;

/** ▶ Run above `func main` and ▶ Run Test above each test function of a _test.lyth file. */
export class RunLenses implements vscode.CodeLensProvider, vscode.Disposable {
    private readonly changed = new vscode.EventEmitter<void>();
    readonly onDidChangeCodeLenses = this.changed.event;

    provideCodeLenses(document: vscode.TextDocument): vscode.CodeLens[] {
        if (!vscode.workspace.isTrusted) {
            return [];
        }
        const isTest = document.uri.path.endsWith('_test.lyth');
        const lenses: vscode.CodeLens[] = [];
        for (let i = 0; i < document.lineCount; i++) {
            const line = document.lineAt(i);
            if (!isTest && MAIN.test(line.text)) {
                lenses.push(new vscode.CodeLens(line.range, { title: '$(play) Run', command: 'lyth.run', arguments: [document.uri] }));
            }
            const test = isTest ? TEST.exec(line.text) : null;
            if (test) {
                lenses.push(new vscode.CodeLens(line.range, { title: '$(play) Run Test', command: 'lyth.runTest', arguments: [document.uri, test[1]] }));
            }
        }
        return lenses;
    }

    refresh(): void {
        this.changed.fire();
    }

    dispose(): void {
        this.changed.dispose();
    }
}
