-- ade-nvim core. No dependencies: pickers use vim.ui.select, so the snacks.nvim picker
-- in the ade-nvim distribution (or any plugin that replaces vim.ui.select) upgrades them.
local M = {}

M.config = { cmd = "ade" }

--- @param opts? { cmd?: string } `cmd` is the ade binary (default "ade").
function M.setup(opts)
  M.config = vim.tbl_extend("force", M.config, opts or {})
end

local function notify(msg, level)
  vim.notify("ade: " .. msg, level or vim.log.levels.INFO)
end

--- Run `ade <args>` asynchronously and hand stdout to cb(err, stdout).
--- @param args string[]
--- @param cb fun(err: string|nil, out: string|nil)
function M.run(args, cb)
  local argv = { M.config.cmd }
  vim.list_extend(argv, args)
  local ok, err = pcall(vim.system, argv, { text = true }, vim.schedule_wrap(function(res)
    if res.code ~= 0 then
      local msg = vim.trim(res.stderr or "")
      cb(msg ~= "" and msg or ("exit code " .. res.code), nil)
      return
    end
    cb(nil, res.stdout)
  end))
  if not ok then
    cb(tostring(err), nil)
  end
end

--- Run `ade <args> --json` and decode the output.
function M.json(args, cb)
  local full = vim.list_extend({}, args)
  table.insert(full, "--json")
  M.run(full, function(err, out)
    if err then
      return cb(err, nil)
    end
    local ok, decoded = pcall(vim.json.decode, out)
    if not ok then
      return cb("invalid JSON from ade: " .. tostring(decoded), nil)
    end
    cb(nil, decoded)
  end)
end

local function weight_label(it)
  if it.weight == nil or it.weight == vim.NIL then
    return "–"
  end
  return tostring(it.weight) .. "w"
end

--- One picker line for a work item: ref, court · reason, weight, title.
function M.format_item(it)
  local court = it.court or ""
  if it.reason and it.reason ~= "" then
    court = court .. " · " .. it.reason
  end
  local title = (it.issue and it.issue.title) or ""
  return string.format("%s  %s  %s  %s", it.ref, court, weight_label(it), title)
end

local function mr_refs(it)
  local refs = {}
  for _, mr in ipairs(it.mrs or {}) do
    table.insert(refs, mr.ref)
  end
  return refs
end

--- Cached work items (`ade items --json`; run `ade sync` to refresh).
function M.fetch_items(cb)
  M.json({ "items" }, cb)
end

--- Pick a work item. Choosing one with merge requests offers to review them.
function M.items()
  M.fetch_items(function(err, items)
    if err then
      return notify(err, vim.log.levels.ERROR)
    end
    if #items == 0 then
      return notify("no cached items; run `ade sync`", vim.log.levels.WARN)
    end
    vim.ui.select(items, { prompt = "ADE work items", format_item = M.format_item }, function(it)
      if not it then
        return
      end
      local refs = mr_refs(it)
      if #refs == 0 then
        return notify(M.format_item(it))
      end
      if #refs == 1 then
        return M.review(refs[1])
      end
      vim.ui.select(refs, { prompt = "Review which MR?" }, function(ref)
        if ref then
          M.review(ref)
        end
      end)
    end)
  end)
end

--- Flatten `ade project status --json` into { label, path } entries.
function M.worktrees(status)
  local out = {}
  for _, project in ipairs(status or {}) do
    for _, repo in ipairs(project.repos or {}) do
      local trees = {}
      if repo.baseline and repo.baseline ~= vim.NIL then
        table.insert(trees, repo.baseline)
      end
      vim.list_extend(trees, repo.worktrees or {})
      for _, t in ipairs(trees) do
        table.insert(out, {
          label = string.format("%s/%s/%s", project.name, repo.name, t.name),
          path = t.path,
        })
      end
    end
  end
  return out
end

local function enter(path)
  if vim.fn.isdirectory(path) == 0 then
    return notify(path .. " is not a directory", vim.log.levels.ERROR)
  end
  vim.cmd.tcd(vim.fn.fnameescape(path))
  vim.cmd.edit(vim.fn.fnameescape(path))
end

--- Make a worktree this tab's working directory and browse it. Without a path,
--- pick one of the registered projects' worktrees.
function M.open(path)
  if path then
    return enter(vim.fn.expand(path))
  end
  M.json({ "project", "status" }, function(err, status)
    if err then
      return notify(err, vim.log.levels.ERROR)
    end
    local trees = M.worktrees(status)
    if #trees == 0 then
      return notify("no worktrees; run `ade project add`", vim.log.levels.WARN)
    end
    vim.ui.select(trees, {
      prompt = "ADE worktrees",
      format_item = function(t)
        return t.label
      end,
    }, function(t)
      if t then
        enter(t.path)
      end
    end)
  end)
end

--- Show an MR (`ade mr show <ref>`) in a terminal split. Returns the buffer.
--- Without a ref, pick from the merge requests of the cached work items.
function M.review(ref)
  if ref then
    vim.cmd("botright new")
    local buf = vim.api.nvim_get_current_buf()
    local argv = { M.config.cmd, "mr", "show", ref }
    if vim.fn.has("nvim-0.11") == 1 then
      vim.fn.jobstart(argv, { term = true })
    else
      vim.fn.termopen(argv) ---@diagnostic disable-line: deprecated
    end
    vim.b[buf].ade_mr = ref
    return buf
  end
  M.fetch_items(function(err, items)
    if err then
      return notify(err, vim.log.levels.ERROR)
    end
    local refs = {}
    for _, it in ipairs(items) do
      vim.list_extend(refs, mr_refs(it))
    end
    if #refs == 0 then
      return notify("no merge requests in the cached items", vim.log.levels.WARN)
    end
    vim.ui.select(refs, { prompt = "Review which MR?" }, function(r)
      if r then
        M.review(r)
      end
    end)
  end)
end

return M
