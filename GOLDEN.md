# Rakuyo

This is the Delirium-specific workflow documentation for Rakuyo.

## Commits

- Use the workstation's auto commit policy for Rakuyo changes: make one
  `msk_git ca` commit per logical change after it is complete and verified,
  subject to the staged/unstaged-worktree exceptions in
  `/home/light/sync/GOLDEN.md`.

## Running servers

- After updating Rakuyo, restart any running Rakuyo servers when the changes
  require it.
- Look in the `rakuyo` window of the `system` tmux session; this is the usual
  location for one or more Rakuyo server panes.
- Restart each affected server in its existing pane, preserving its command and
  arguments. For example, send Ctrl-C through tmux and rerun the pane's command.
- Verify each restarted server is listening and serving requests before
  considering the update complete.
