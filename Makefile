# Root Makefile: only includes. Do not add targets here.
# Shared targets live in make/common.mk; each component adds make/<component>.mk (CLAUDE.md).

include make/common.mk
-include $(filter-out make/common.mk,$(sort $(wildcard make/*.mk)))
