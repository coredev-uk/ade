-- ade-nvim distribution config: loaded by `nix run .#ade-nvim` (NVIM_APPNAME=ade-nvim),
-- never by a normal Neovim. Plugins come pinned from nixpkgs; nothing is downloaded.
-- Space-leader. Finding and git follow the common snacks.nvim layout (<Leader>f…, <Leader>g…,
-- <Leader>e); ADE's own commands live under <Leader>a.
vim.g.mapleader = " "
vim.g.maplocalleader = " "
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

-- snacks.nvim: the picker (also behind vim.ui.select, so ADE's own lists use it),
-- the explorer, lazygit, git browse.
try("snacks", function(m)
  m.setup({
    picker = { enabled = true, ui_select = true },
    explorer = { enabled = true },
    lazygit = { enabled = true },
    gitbrowse = { enabled = true },
    bigfile = { enabled = true },
    input = { enabled = true },
    notifier = { enabled = true },
  })
end)

-- mini.nvim: diff signs, git, clue, statusline, completion.
try("mini.diff", function(m) m.setup() end)
try("mini.git", function(m) m.setup() end)
try("mini.statusline", function(m) m.setup() end)
try("mini.completion", function(m) m.setup() end)
try("mini.clue", function(m)
  m.setup({
    triggers = { { mode = "n", keys = "<Leader>" }, { mode = "n", keys = "g" }, { mode = "n", keys = "]" }, { mode = "n", keys = "[" } },
    clues = {
      m.gen_clues.g(),
      m.gen_clues.builtin_completion(),
      { mode = "n", keys = "<Leader>f", desc = "+find" },
      { mode = "n", keys = "<Leader>g", desc = "+git" },
      { mode = "n", keys = "<Leader>l", desc = "+lsp" },
      { mode = "n", keys = "<Leader>a", desc = "+ade" },
    },
  })
end)

local map = vim.keymap.set

-- pick(name): a snacks picker by name; netrw when snacks is missing.
local function pick(name)
  return function()
    local ok, s = pcall(require, "snacks")
    if ok then
      s.picker[name]()
    else
      vim.cmd.Explore()
    end
  end
end

for _, k in ipairs({
  { "<Leader><Leader>", "smart", "Smart find (files, buffers, recent)" },
  { "<Leader>ff", "files", "Find files" },
  { "<Leader>fg", "grep", "Live grep" },
  { "<Leader>fb", "buffers", "Buffers" },
  { "<Leader>fh", "help", "Help tags" },
  { "<Leader>fr", "recent", "Recent files" },
  { "<Leader>f/", "grep_buffers", "Grep open buffers" },
  { "<Leader>fw", "grep_word", "Grep word under cursor" },
  { "<Leader>fm", "marks", "Marks" },
  { "<Leader>fk", "keymaps", "Keymaps" },
  { "<Leader>f:", "command_history", "Command history" },
  { "<Leader>fd", "diagnostics", "Diagnostics" },
  { "<Leader>fD", "diagnostics_buffer", "Buffer diagnostics" },
  { "<Leader>fR", "resume", "Resume last picker" },
  { "<Leader>fs", "lsp_symbols", "LSP document symbols" },
  { "<Leader>fS", "lsp_workspace_symbols", "LSP workspace symbols" },
  { "<Leader>ft", "treesitter", "Treesitter symbols" },
  { "<Leader>fu", "undo", "Undo history" },
  { '<Leader>f"', "registers", "Registers" },
  { "<Leader>fq", "qflist", "Quickfix list" },
  { "<Leader>gl", "git_log", "Git log" },
  { "<Leader>gL", "git_log_file", "Git log (current file)" },
  { "<Leader>gb", "git_branches", "Git branches" },
  { "<Leader>gs", "git_status", "Git status" },
  { "<Leader>gS", "git_stash", "Git stash" },
  { "<Leader>gd", "git_diff", "Git diff" },
}) do
  map("n", k[1], pick(k[2]), { desc = k[3] })
end
map("n", "<Leader>e", function()
  if pcall(require, "snacks") then require("snacks").explorer() else vim.cmd.Explore() end
end, { desc = "File explorer" })
map("n", "<Leader>gg", function() require("snacks").lazygit() end, { desc = "LazyGit" })
map("n", "<Leader>gB", function() require("snacks").gitbrowse() end, { desc = "Git browse (open in browser)" })

-- ADE (see the dashboard's `?`): items, worktree, review.
map("n", "<Leader>ai", "<Cmd>AdeItems<CR>", { desc = "ADE work items" })
map("n", "<Leader>aw", "<Cmd>AdeOpen<CR>", { desc = "ADE worktree" })
map("n", "<Leader>ar", "<Cmd>AdeReview<CR>", { desc = "ADE review an MR" })

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
map("n", "<Leader>la", vim.lsp.buf.code_action, { desc = "Code action" })
map("n", "<Leader>ln", vim.lsp.buf.rename, { desc = "Rename symbol" })
map("n", "<Leader>lf", function() vim.lsp.buf.format({ async = true }) end, { desc = "Format" })
map("n", "<Leader>lh", vim.lsp.buf.hover, { desc = "Hover" })
map("n", "<Leader>ls", vim.lsp.buf.signature_help, { desc = "Signature help" })
map("n", "<Leader>le", vim.diagnostic.open_float, { desc = "Diagnostic float" })
map("n", "gd", pick("lsp_definitions"), { desc = "Go to definition" })
map("n", "gD", pick("lsp_declarations"), { desc = "Go to declaration" })
map("n", "gr", pick("lsp_references"), { desc = "References", nowait = true })
map("n", "gI", pick("lsp_implementations"), { desc = "Implementations" })
map("n", "gy", pick("lsp_type_definitions"), { desc = "Type definition" })
