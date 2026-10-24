// Runs the integration tests in VS Code on a copy of test/fixtures, a Go module with Lyth packages.
// VSCODE_EXECUTABLE picks the VS Code to run (default: download the latest stable release);
// LYTH_PATH picks the `lyth` command (default: built from this repository).
import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { runTests } from '@vscode/test-electron';

async function main(): Promise<void> {
    const extension = path.resolve(__dirname, '../..');
    const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'lyth-vscode-'));
    fs.cpSync(path.join(extension, 'test/fixtures'), workspace, { recursive: true });
    let lyth = process.env.LYTH_PATH && path.resolve(process.env.LYTH_PATH);
    if (!lyth) {
        lyth = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'lyth-bin-')), process.platform === 'win32' ? 'lyth.exe' : 'lyth');
        execFileSync('go', ['build', '-o', lyth, './cmd/lyth'], { cwd: path.resolve(extension, '../..'), stdio: 'inherit' });
    }
    const settings: Record<string, unknown> = { 'lyth.path': lyth, 'lyth.vet': false };
    if (process.env.LYTH_TEST_TRACE) {
        settings['lyth.trace.server'] = 'verbose';
    }
    fs.mkdirSync(path.join(workspace, '.vscode'));
    fs.writeFileSync(path.join(workspace, '.vscode/settings.json'), JSON.stringify(settings, null, 2));
    try {
        await runTests({
            vscodeExecutablePath: process.env.VSCODE_EXECUTABLE || undefined,
            extensionDevelopmentPath: extension,
            extensionTestsPath: path.resolve(__dirname, 'suite/index'),
            launchArgs: [workspace, '--disable-extensions'],
        });
    } finally {
        fs.rmSync(workspace, { recursive: true, force: true });
    }
}

main().catch((e) => {
    console.error(e);
    process.exit(1);
});
