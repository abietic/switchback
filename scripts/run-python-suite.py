#!/usr/bin/env python3
"""Run one stdlib suite, rejecting empty discovery, skips and failures."""
import sys
import unittest

if len(sys.argv) != 2:
    raise SystemExit("usage: run-python-suite.py TEST_DIRECTORY")
suite = unittest.defaultTestLoader.discover(sys.argv[1], pattern="test_*.py")
result = unittest.TextTestRunner(verbosity=2).run(suite)
print("python suite: tests={} skipped={}".format(result.testsRun, len(result.skipped)))
raise SystemExit(0 if result.testsRun > 0 and not result.skipped and result.wasSuccessful() else 1)
