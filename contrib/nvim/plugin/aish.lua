-- Inside aish, the diff of the files this neovim saved goes to its agent;
-- elsewhere nothing happens. See lua/aish/init.lua. A setup the user's
-- config has called already, with its options, stays.
if vim.g.loaded_aish then
	return
end
vim.g.loaded_aish = true
local aish = require("aish")
if not aish.configured then
	aish.setup()
end
