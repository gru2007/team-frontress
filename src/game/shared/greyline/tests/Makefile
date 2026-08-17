# GREYLINE FRONTRESS — tests for the game-side logic.
#
#   make        build and run everything
#   make build  build only
#   make asan   run under AddressSanitizer and UBSan
#
# No Source engine, no VPC, no dependencies: greyline_briefing_logic.cpp
# deliberately includes nothing from the tree, which is what lets these run in
# CI on a plain Linux box in about a second.

CXX      ?= g++
CXXFLAGS ?= -std=c++11 -O1 -g -Wall -Wextra -Werror -Wno-unused-parameter
BIN      := bin
REPO     := ../../../../..
RESOURCE := $(REPO)/game/tc2/loose/resource

LOGIC := ../greyline_briefing_logic.cpp

TESTS := $(BIN)/greyline_logic_test $(BIN)/greyline_localization_test

.PHONY: all build test asan clean

all: test

build: $(TESTS)

$(BIN):
	@mkdir -p $(BIN)

$(BIN)/greyline_logic_test: greyline_logic_test.cpp $(LOGIC) greyline_test.h | $(BIN)
	$(CXX) $(CXXFLAGS) -o $@ greyline_logic_test.cpp $(LOGIC)

$(BIN)/greyline_localization_test: greyline_localization_test.cpp $(LOGIC) greyline_test.h | $(BIN)
	$(CXX) $(CXXFLAGS) -o $@ greyline_localization_test.cpp $(LOGIC)

test: $(TESTS)
	@$(BIN)/greyline_logic_test
	@echo
	@$(BIN)/greyline_localization_test $(RESOURCE)

# The logic layer indexes fixed-size arrays from data a coordinator sent, so it
# is worth running once with the sanitizers on rather than trusting the reading.
asan: CXXFLAGS += -fsanitize=address,undefined -fno-omit-frame-pointer
asan: clean test

clean:
	@rm -rf $(BIN)
