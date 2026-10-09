-- aish: inside aish (its shell has $AISH_RUN), tell its agent what you
-- changed in files. When neovim exits, or is stopped with Ctrl+Z, the
-- unified diff of what it saved, against the files as they were before,
-- goes to $AISH_RUN/edits; aish puts it in the journal with the command
-- that ran neovim, and the model reads it with the command's output.
--
-- Only what was written counts: unsaved changes are not told of. A file is
-- remembered as it was when neovim first read it, or, for one it never
-- read, right before it first wrote it; a file read and gone by the end is
-- told of as deleted. Files in temporary directories are left out by
-- default: tools that hand a secret to $EDITOR (pass edit, sops,
-- kubectl edit) put it there.
local M = {}

local uv = vim.uv or vim.loop
local diff = (vim.text and vim.text.diff) or vim.diff

-- A file bigger than this is not kept to diff: that it changed is told
-- without its lines.
local max_file = 4 * 1024 * 1024

local state = {
	dir = nil, -- $AISH_RUN/edits; nil outside aish
	ignore = {}, -- directories whose files are not told of, their links resolved too
	-- What each file was, by its full path: text, its content (false: there
	-- was none; nil: too big or not readable), and whether neovim wrote it.
	files = {},
	seq = 0, -- diffs written, for their names
}

-- canon is the file's full path with links resolved, so that one file
-- opened by two names is one; a file not there yet gets its directory's.
local function canon(path)
	local real = uv.fs_realpath(path)
	if real then
		return real
	end
	local dir = uv.fs_realpath(vim.fs.dirname(path))
	if dir then
		return dir .. "/" .. vim.fs.basename(path)
	end
	return path
end

local function ignored(path)
	if path == "" or path:match("^%a[%w+.-]*://") then
		return true -- not a local file: oil://, fugitive://, scp://
	end
	for _, d in ipairs(state.ignore) do
		if path == d or path:sub(1, #d + 1) == d .. "/" then
			return true
		end
	end
	return false
end

-- read is the file's content, false when there is no file, or nil when it
-- is too big or cannot be read; the size goes with it.
local function read(path)
	local st = uv.fs_stat(path)
	if not st then
		return false, 0
	end
	if st.type ~= "file" or st.size > max_file then
		return nil, st.size
	end
	local f = io.open(path, "rb")
	if not f then
		return nil, st.size
	end
	local s = f:read("*a")
	f:close()
	return s, st.size
end

local function track(name)
	local path = canon(name)
	if state.files[path] or ignored(path) then
		return
	end
	state.files[path] = { text = (read(path)) }
end

local function wrote(name)
	local f = state.files[canon(name)]
	if f then
		f.written = true
	end
end

-- change is the unified diff of one file, nil when it did not change.
local function change(path, before, after)
	local from = before == false and "/dev/null" or path
	local to = after == false and "/dev/null" or path
	local head = "--- " .. from .. "\n+++ " .. to .. "\n"
	if before == nil or after == nil then
		return head .. "[too large or not readable to diff]\n"
	end
	if before == after then
		return nil
	end
	local a, b = before or "", after or ""
	if a:find("\0", 1, true) or b:find("\0", 1, true) then
		return "Binary files " .. from .. " and " .. to .. " differ\n"
	end
	local hunks = diff(a, b, { ctxlen = 3, indent_heuristic = true })
	if hunks == "" then
		return nil
	end
	return head .. hunks
end

-- save leaves text in the directory aish takes it from, whole or not at
-- all: aish reads the file the moment the command is over.
local function save(text)
	if text == "" or not uv.fs_stat(vim.fs.dirname(state.dir)) then
		return -- aish is gone
	end
	uv.fs_mkdir(state.dir, 448) -- 0700; there already, it fails
	state.seq = state.seq + 1
	local name = string.format("%d-%d", uv.os_getpid(), state.seq)
	local tmp = state.dir .. "/." .. name .. ".tmp"
	local fd = uv.fs_open(tmp, "w", 384) -- 0600: the lines of the user's files
	if not fd then
		return
	end
	local n = uv.fs_write(fd, text, 0)
	uv.fs_close(fd)
	if n == #text then
		uv.fs_rename(tmp, state.dir .. "/" .. name .. ".diff")
	else
		uv.fs_unlink(tmp)
	end
end

-- flush tells aish what was saved since the start, or since the last
-- flush: after Ctrl+Z, `fg` gets only what came after it.
local function flush()
	local paths = vim.tbl_keys(state.files)
	table.sort(paths)
	local out = {}
	for _, path in ipairs(paths) do
		local f = state.files[path]
		if f.written or (f.text ~= false and not uv.fs_stat(path)) then
			local now = read(path)
			local d = change(path, f.text, now)
			if d then
				table.insert(out, d)
			end
			state.files[path] = { text = now }
		end
	end
	save(table.concat(out))
end

local function guard(fn)
	return function(a)
		pcall(fn, a.match)
	end
end

local function defaults()
	local dirs = { "/tmp", "/var/tmp", "/dev/shm" }
	for _, v in ipairs({ "TMPDIR", "XDG_RUNTIME_DIR" }) do
		if vim.env[v] and vim.env[v] ~= "" then
			table.insert(dirs, vim.env[v])
		end
	end
	return dirs
end

--- setup starts telling aish of the files saved, inside aish only; called
--- again, it takes the new options. opts.ignore: the directories whose
--- files are left out, instead of the temporary ones ({} for none).
function M.setup(opts)
	M.configured = true
	opts = opts or {}
	local run = vim.env.AISH_RUN
	local st = run and run ~= "" and uv.fs_stat(run)
	if not st or st.type ~= "directory" then
		return
	end
	state.dir = run .. "/edits"
	state.ignore = {}
	for _, d in ipairs(opts.ignore or defaults()) do
		d = vim.fs.normalize(d):gsub("/+$", "")
		if d ~= "" then
			table.insert(state.ignore, d)
			local real = uv.fs_realpath(d)
			if real and real ~= d then
				table.insert(state.ignore, real)
			end
		end
	end
	local group = vim.api.nvim_create_augroup("aish", { clear = true })
	vim.api.nvim_create_autocmd("BufReadPost", {
		group = group,
		callback = function(a)
			if vim.bo[a.buf].buftype == "" then
				pcall(track, a.match)
			end
		end,
	})
	vim.api.nvim_create_autocmd({ "BufWritePre", "FileWritePre", "FileAppendPre" }, { group = group, callback = guard(track) })
	vim.api.nvim_create_autocmd({ "BufWritePost", "FileWritePost", "FileAppendPost" }, { group = group, callback = guard(wrote) })
	vim.api.nvim_create_autocmd({ "VimSuspend", "VimLeavePre" }, {
		group = group,
		callback = function()
			pcall(flush)
		end,
	})
	-- Loaded late (lazily), it takes the files already read as they are on
	-- disk: changes not saved yet are not there.
	for _, b in ipairs(vim.api.nvim_list_bufs()) do
		if vim.api.nvim_buf_is_loaded(b) and vim.bo[b].buftype == "" then
			local name = vim.api.nvim_buf_get_name(b)
			if name ~= "" then
				pcall(track, name)
			end
		end
	end
end

return M
