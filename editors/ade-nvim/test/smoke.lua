-- Headless smoke test:
--   ADE_BIN=/path/to/ade [ADE_SMOKE_WORKTREE=<path>] nvim --headless -u NONE -i NONE \
--     --cmd 'set rtp^=editors/ade-nvim' -l editors/ade-nvim/test/smoke.lua
-- The ade binary must see a synced cache (`ade sync`) and the demo project. Exits 1 on failure.
local failures = {}
local function check(desc, ok, detail)
  if ok then
    print("  PASS  " .. desc)
  else
    print("  FAIL  " .. desc .. (detail and (" (" .. tostring(detail) .. ")") or ""))
    table.insert(failures, desc)
  end
end

local function wait_for(pred, timeout_ms)
  return vim.wait(timeout_ms or 30000, pred, 50)
end

vim.cmd("runtime plugin/ade.lua")
local ade = require("ade")
ade.setup({ cmd = assert(os.getenv("ADE_BIN"), "set ADE_BIN") })

check(":AdeItems, :AdeOpen and :AdeReview are defined",
  vim.fn.exists(":AdeItems") == 2 and vim.fn.exists(":AdeOpen") == 2 and vim.fn.exists(":AdeReview") == 2)

-- items --json -> picker lines
local items, items_err, done
ade.fetch_items(function(err, out)
  items, items_err, done = out, err, true
end)
check("ade items --json answers", wait_for(function() return done end) and items_err == nil, items_err)
local tracker1
for _, it in ipairs(items or {}) do
  if it.ref == "ade-demo-env/tracker#1" then
    tracker1 = it
  end
end
check("items include the seeded tracker#1", tracker1 ~= nil)
if tracker1 then
  local line = ade.format_item(tracker1)
  check("picker line names ref, court and title",
    line:find("tracker#1", 1, true) and line:find("Rate-limit", 1, true) and line:find("you", 1, true), line)
end

-- :AdeItems chooses tracker#1 (one MR) and shows it in a terminal split
local prompts = {}
local real_select = vim.ui.select
vim.ui.select = function(list, opts, on_choice)
  table.insert(prompts, opts.prompt)
  for _, entry in ipairs(list) do
    if type(entry) == "table" and entry.ref == "ade-demo-env/tracker#1" then
      return on_choice(entry)
    end
  end
  on_choice(nil)
end
vim.cmd("AdeItems")
local review_buf
check(":AdeItems -> :AdeReview opens `ade mr show` for the item's MR", wait_for(function()
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    if vim.b[b].ade_mr == "ade-demo-env/api!1" then
      review_buf = b
      return true
    end
  end
end), table.concat(prompts, ","))
if review_buf then
  local shown = wait_for(function()
    return table.concat(vim.api.nvim_buf_get_lines(review_buf, 0, -1, false), "\n"):find("api!1", 1, true) ~= nil
  end)
  check("the MR summary is printed in the terminal buffer", shown)
end
vim.ui.select = real_select

-- :AdeOpen <path> changes the tab's working directory
local dir = vim.fn.resolve(vim.fn.tempname())
vim.fn.mkdir(dir, "p")
vim.cmd("AdeOpen " .. vim.fn.fnameescape(dir))
check(":AdeOpen <path> sets the working directory", vim.fn.resolve(vim.fn.getcwd()) == dir, vim.fn.getcwd())

-- :AdeOpen (no argument) picks among `ade project status` worktrees
local want = os.getenv("ADE_SMOKE_WORKTREE")
if want then
  vim.ui.select = function(list, _, on_choice)
    for _, entry in ipairs(list) do
      if entry.path == want then
        return on_choice(entry)
      end
    end
    on_choice(nil)
  end
  vim.cmd("AdeOpen")
  check(":AdeOpen picks a registered worktree", wait_for(function()
    return vim.fn.resolve(vim.fn.getcwd()) == vim.fn.resolve(want)
  end), vim.fn.getcwd())
  vim.ui.select = real_select
end

-- a missing binary is reported, never thrown
ade.setup({ cmd = "/nonexistent/ade" })
local err2, done2
ade.fetch_items(function(err) err2, done2 = err, true end)
check("a missing ade binary is an error message", wait_for(function() return done2 end, 5000) and err2 ~= nil)

if #failures > 0 then
  print(string.format("ade-nvim smoke: %d failure(s)", #failures))
  vim.cmd("cquit 1")
end
print("ade-nvim smoke: all checks passed")
vim.cmd("qa!")
