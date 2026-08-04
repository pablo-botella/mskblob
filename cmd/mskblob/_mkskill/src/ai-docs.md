---
mkskill:
  pos: 90
---

## Generate the AI docs

`SKILL.md` (Claude Code) and `AGENTS.md` (tool-agnostic) are composed from the `ai/` sources — edit those, not the generated files, then re-run:

```
mskblob generate-agent-docs                  # → AGENTS.md
mskblob generate-claude-skill                # → .claude/skills/mskblob/SKILL.md (project-local)
mskblob generate-claude-skill -global        # → ~/.claude/skills/mskblob/SKILL.md (every project)
```

`-global` installs the skill under your own home, so it needs **no elevation/admin**; it is mutually exclusive with `-dst`. Use `-force` to overwrite an existing destination.

