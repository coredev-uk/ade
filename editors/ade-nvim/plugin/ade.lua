-- ade-nvim: ADE's Neovim companion. Loaded automatically when the directory is on
-- the runtimepath; every command is a thin wrapper over the `ade` CLI.
if vim.g.loaded_ade_nvim == 1 then
  return
end
vim.g.loaded_ade_nvim = 1

vim.api.nvim_create_user_command("AdeItems", function()
  require("ade").items()
end, { desc = "Pick an ADE work item (ade items --json)" })

vim.api.nvim_create_user_command("AdeOpen", function(opts)
  require("ade").open(opts.args ~= "" and opts.args or nil)
end, { nargs = "?", complete = "dir", desc = "Open an ADE worktree as the tab's working directory" })

vim.api.nvim_create_user_command("AdeReview", function(opts)
  require("ade").review(opts.args ~= "" and opts.args or nil)
end, { nargs = "?", desc = "Show a merge request (ade mr show <ref>)" })
