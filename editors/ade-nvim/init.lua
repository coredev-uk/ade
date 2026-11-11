-- ade-nvim distribution config: loaded by `nix run .#ade-nvim` (NVIM_APPNAME=ade-nvim),
-- never by a normal Neovim. Plugins come pinned from nixpkgs; nothing is downloaded.
-- The keymap mirrors the ADE dashboard: space-leader, one mnemonic per ADE key.
vim.g.mapleader = " "
local o = vim.o
o.number, o.relativenumber, o.signcolumn = true, true, "yes"
o.termguicolors, o.mouse, o.undofile = true, "a", true
o.ignorecase, o.smartcase = true, true
o.splitright, o.splitbelow = true, true
o.updatetime = 250

local function try(mod, setup)
  local ok, m = pcall(require, mod)
  if ok then
    setup(m)
  end
end

-- mini.nvim: pick, files, diff, git, clue, statusline, completion.
try("mini.pick", function(m) m.setup() vim.ui.select = m.ui_select end)
try("mini.files", function(m) m.setup() end)
try("mini.diff", function(m) m.setup() end)
try("mini.git", function(m) m.setup() end)
try("mini.statusline", function(m) m.setup() end)
try("mini.completion", function(m) m.setup() end)
try("mini.clue", function(m)
  m.setup({
    triggers = { { mode = "n", keys = "<Leader>" }, { mode = "n", keys = "g" }, { mode = "n", keys = "]" }, { mode = "n", keys = "[" } },
    clues = { m.gen_clues.g(), m.gen_clues.builtin_completion() },
  })
end)

-- ADE keys (see the dashboard's `?`): i items, w workspace, r review, f files, / grep.
local map = vim.keymap.set
map("n", "<Leader>i", "<Cmd>AdeItems<CR>", { desc = "ADE work items" })
map("n", "<Leader>w", "<Cmd>AdeOpen<CR>", { desc = "ADE worktree" })
map("n", "<Leader>r", "<Cmd>AdeReview<CR>", { desc = "ADE review an MR" })
map("n", "<Leader>f", function()
  if pcall(require, "mini.pick") then require("mini.pick").builtin.files() else vim.cmd.Explore() end
end, { desc = "Files" })
map("n", "<Leader>/", function()
  if pcall(require, "mini.pick") then require("mini.pick").builtin.grep_live() end
end, { desc = "Grep" })
map("n", "<Leader>e", function()
  if pcall(require, "mini.files") then require("mini.files").open() else vim.cmd.Explore() end
end, { desc = "Explorer" })

-- Tree-sitter highlighting with the prebuilt grammars on the runtimepath.
vim.api.nvim_create_autocmd("FileType", {
  callback = function(ev)
    pcall(vim.treesitter.start, ev.buf)
  end,
})

-- Native LSP: nvim-lspconfig ships the server definitions; the servers themselves
-- come from each project's devshell, so only those on PATH are enabled.
for server, binary in pairs({
  gopls = "gopls",
  lua_ls = "lua-language-server",
  ts_ls = "typescript-language-server",
  pyright = "pyright-langserver",
  rust_analyzer = "rust-analyzer",
  nixd = "nixd",
}) do
  if vim.fn.executable(binary) == 1 then
    pcall(vim.lsp.enable, server)
  end
end
map("n", "<Leader>d", vim.diagnostic.open_float, { desc = "Diagnostics" })
