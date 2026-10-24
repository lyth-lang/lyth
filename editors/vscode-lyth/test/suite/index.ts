import * as path from 'node:path';
import Mocha from 'mocha';

export function run(): Promise<void> {
    // MOCHA_GREP runs only the tests whose name matches
    const mocha = new Mocha({ ui: 'tdd', color: true, timeout: 60_000, grep: process.env.MOCHA_GREP });
    mocha.addFile(path.join(__dirname, 'extension.test.js'));
    return new Promise((resolve, reject) => {
        mocha.run((failures) => (failures ? reject(new Error(`${failures} test(s) failed`)) : resolve()));
    });
}
