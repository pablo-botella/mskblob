# Makefile — builds mskblob-c with gcc/MinGW or clang.
#   make            64-bit (or whatever the compiler defaults to)
#   make CFLAGS+=-m32   32-bit, when the toolchain supports it
CC      ?= gcc
CFLAGS  ?= -std=c99 -Wall -Wextra -pedantic -O2
LDFLAGS ?=
EXE     := mskblob-c$(if $(findstring Windows_NT,$(OS)),.exe,)

all: $(EXE)

$(EXE): mskblob.c mskblob_cli.c mskblob.h
	$(CC) $(CFLAGS) -o $@ mskblob.c mskblob_cli.c $(LDFLAGS)

clean:
	rm -f $(EXE) *.o *.obj

.PHONY: all clean
