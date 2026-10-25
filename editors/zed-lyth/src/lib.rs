//! Starts the Lyth language server, `lyth lsp`, for Zed.

use zed_extension_api::{self as zed, settings::LspSettings, LanguageServerId, Result};

struct LythExtension;

impl zed::Extension for LythExtension {
    fn new() -> Self {
        LythExtension
    }

    fn language_server_command(
        &mut self,
        language_server_id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<zed::Command> {
        // "lsp": { "lyth": { "binary": { "path", "arguments", "env" } } } in Zed's settings
        // overrides the command; otherwise `lyth` is found on the worktree's PATH.
        let binary = LspSettings::for_worktree(language_server_id.as_ref(), worktree)
            .ok()
            .and_then(|settings| settings.binary);
        let args = binary
            .as_ref()
            .and_then(|binary| binary.arguments.clone())
            .unwrap_or_else(|| vec!["lsp".to_string()]);
        let mut env = worktree.shell_env();
        if let Some(extra) = binary.as_ref().and_then(|binary| binary.env.clone()) {
            env.extend(extra);
        }
        let command = match binary.and_then(|binary| binary.path) {
            Some(path) => path,
            None => worktree.which("lyth").ok_or_else(|| {
                "lyth is not on PATH. Install it with `brew install lyth-lang/tap/lyth` or \
                 `go install github.com/lyth-lang/lyth/cmd/lyth@latest`, or set \
                 lsp.lyth.binary.path in Zed's settings."
                    .to_string()
            })?,
        };
        Ok(zed::Command { command, args, env })
    }
}

zed::register_extension!(LythExtension);
