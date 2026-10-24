import * as assert from 'node:assert';
import * as vscode from 'vscode';

const root = vscode.workspace.workspaceFolders![0].uri;

async function open(file: string): Promise<vscode.TextDocument> {
    const document = await vscode.workspace.openTextDocument(vscode.Uri.joinPath(root, file));
    await vscode.window.showTextDocument(document);
    return document;
}

/** The position of the `nth` occurrence of `text`, plus `offset` characters. */
function positionOf(document: vscode.TextDocument, text: string, offset = 0, nth = 1): vscode.Position {
    let at = -1;
    for (let i = 0; i < nth; i++) {
        at = document.getText().indexOf(text, at + 1);
        assert.ok(at >= 0, `${text} #${nth} not found`);
    }
    return document.positionAt(at + offset);
}

/** Retries `get` until it returns something other than false, undefined or an empty array. */
async function eventually<T>(what: string, get: () => T | undefined | Thenable<T | undefined>, timeout = 30_000): Promise<T> {
    const end = Date.now() + timeout;
    for (;;) {
        const value = await get();
        if (value && (!Array.isArray(value) || value.length)) {
            return value;
        }
        if (Date.now() > end) {
            throw new Error(`timed out waiting for ${what}`);
        }
        await new Promise((resolve) => setTimeout(resolve, 200));
    }
}

const label = (item: vscode.CompletionItem) => (typeof item.label === 'string' ? item.label : item.label.label);
const hoverText = (hovers: vscode.Hover[]) => hovers.flatMap((h) => h.contents.map((c) => (typeof c === 'string' ? c : c.value))).join('\n');

suite('Lyth extension', () => {
    suiteSetup(async () => {
        await vscode.extensions.getExtension('lyth-lang.lyth')!.activate();
    });

    test('.lyth files are Lyth', async () => {
        const document = await open('main.lyth');
        assert.strictEqual(document.languageId, 'lyth');
    });

    test('errors are reported by the server', async () => {
        const document = await open('broken/broken.lyth');
        const diagnostics = await eventually('diagnostics', () => {
            const found = vscode.languages.getDiagnostics(document.uri);
            return found.length ? found : undefined;
        });
        assert.ok(diagnostics.some((d) => d.source === 'lyth' && d.severity === vscode.DiagnosticSeverity.Error && d.range.start.line === 1), JSON.stringify(diagnostics));
    });

    test('hover shows the declaration and the generated Go', async () => {
        const document = await open('main.lyth');
        const hovers = await eventually('hover', () =>
            vscode.commands.executeCommand<vscode.Hover[]>('vscode.executeHoverProvider', document.uri, positionOf(document, 'double(21)', 1)),
        );
        const text = hoverText(hovers);
        assert.match(text, /func double\(n: int\): int/);
        assert.match(text, /func double\(n int\) int/);
    });

    test('go to definition in another package', async () => {
        const document = await open('main.lyth');
        const locations = await eventually('definition', () =>
            vscode.commands.executeCommand<vscode.Location[]>('vscode.executeDefinitionProvider', document.uri, positionOf(document, 'Greet', 1)),
        );
        assert.strictEqual(locations.length, 1);
        // the URI the editor uses, even though the test workspace's path has a symlink on macOS
        assert.strictEqual(locations[0].uri.toString(), vscode.Uri.joinPath(root, 'lib/lib.lyth').toString());
        assert.strictEqual(locations[0].range.start.line, 1);
    });

    test('document symbols', async () => {
        const document = await open('main.lyth');
        const symbols = await eventually('symbols', () => vscode.commands.executeCommand<vscode.DocumentSymbol[]>('vscode.executeDocumentSymbolProvider', document.uri));
        assert.deepStrictEqual(symbols.map((s) => s.name), ['double', 'main']);
    });

    test('completion has declarations, and each snippet once', async () => {
        const document = await open('main.lyth');
        const list = await vscode.commands.executeCommand<vscode.CompletionList>('vscode.executeCompletionItemProvider', document.uri, positionOf(document, '    fmt.Println', 4));
        assert.ok(list.items.some((i) => label(i) === 'double' && i.kind === vscode.CompletionItemKind.Function));
        assert.ok(list.items.some((i) => label(i) === 'total' && i.kind === vscode.CompletionItemKind.Variable));
        const mainSnippets = list.items.filter((i) => label(i) === 'main' && i.kind === vscode.CompletionItemKind.Snippet);
        assert.strictEqual(mainSnippets.length, 1);
    });

    test('rename', async () => {
        const document = await open('main.lyth');
        const edit = await vscode.commands.executeCommand<vscode.WorkspaceEdit>('vscode.executeDocumentRenameProvider', document.uri, positionOf(document, 'double(21)'), 'twice');
        assert.strictEqual(edit.get(document.uri).length, 2);
    });

    test('inlay hints show inferred types', async () => {
        const document = await open('main.lyth');
        const hints = await vscode.commands.executeCommand<vscode.InlayHint[]>('vscode.executeInlayHintProvider', document.uri, new vscode.Range(0, 0, document.lineCount, 0));
        const labels = hints.map((h) => `${h.position.line}${typeof h.label === 'string' ? h.label : h.label.map((p) => p.value).join('')}`);
        assert.deepStrictEqual(labels, ['3: int', '6: int']);
    });

    test('run and test code lenses', async () => {
        const main = await open('main.lyth');
        const lenses = await vscode.commands.executeCommand<vscode.CodeLens[]>('vscode.executeCodeLensProvider', main.uri);
        assert.deepStrictEqual(lenses.map((l) => [l.range.start.line, l.command?.command]), [[positionOf(main, 'func main').line, 'lyth.run']]);
        const tests = await open('lib/lib_test.lyth');
        const testLenses = await vscode.commands.executeCommand<vscode.CodeLens[]>('vscode.executeCodeLensProvider', tests.uri);
        assert.deepStrictEqual(testLenses.map((l) => [l.command?.command, ...(l.command?.arguments ?? []).slice(1)]), [['lyth.runTest', 'TestGreet']]);
    });

    test('generated Go view follows edits', async () => {
        const document = await open('main.lyth');
        await vscode.commands.executeCommand('lyth.showGeneratedGo', document.uri);
        const view = await eventually('generated Go', () => vscode.workspace.textDocuments.find((d) => d.uri.scheme === 'lyth-go'));
        assert.strictEqual(view.uri.path, `${document.uri.path}.go`);
        assert.strictEqual(view.languageId, 'go');
        assert.match(view.getText(), /func double\(n int\) int/);
        const edit = new vscode.WorkspaceEdit();
        edit.insert(document.uri, new vscode.Position(document.lineCount, 0), '\nfunc added(): int = 42\n');
        await vscode.workspace.applyEdit(edit);
        await eventually('updated view', () => /func added\(\) int/.test(view.getText()));
        await vscode.window.showTextDocument(document);
        await vscode.commands.executeCommand('workbench.action.files.revert');
    });

    test('tasks for the module', async () => {
        const tasks = await vscode.tasks.fetchTasks({ type: 'lyth' });
        assert.deepStrictEqual(tasks.map((t) => t.name).sort(), ['build', 'check', 'run', 'test', 'vet']);
        const build = tasks.find((t) => t.name === 'build')!;
        assert.strictEqual(build.group?.id, vscode.TaskGroup.Build.id);
        assert.deepStrictEqual((build.execution as vscode.ProcessExecution).args, ['build', './...']);
    });

    test('```lyth blocks in Markdown are highlighted', async () => {
        const tokens = await vscode.commands.executeCommand<{ c: string; t: string }[]>('_workbench.captureSyntaxTokens', vscode.Uri.joinPath(root, 'notes.md'));
        const func = tokens.find((t) => t.c === 'func');
        assert.ok(func, 'no `func` token');
        assert.match(func.t, /meta\.embedded\.block\.lyth .*keyword\.function\.lyth/);
    });

    test('the server restarts', async () => {
        await vscode.commands.executeCommand('lyth.restartServer');
        const document = await open('main.lyth');
        const symbols = await eventually('symbols after restart', () =>
            vscode.commands.executeCommand<vscode.DocumentSymbol[]>('vscode.executeDocumentSymbolProvider', document.uri),
        );
        assert.ok(symbols.some((s) => s.name === 'main'));
    });
});
