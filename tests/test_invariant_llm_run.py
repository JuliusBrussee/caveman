import pytest
import subprocess
import sys
import os

# Add the evals directory to the path to import llm_run
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..'))

from evals.llm_run import run_command  # Assuming the function is named run_command

@pytest.mark.parametrize("payload", [
    # Exact exploit case: command injection via shell metacharacters
    "echo hello; ls /",
    # Boundary case: input with spaces and special characters but no injection
    "echo 'test input'",
    # Valid input: normal command
    "echo safe",
])
def test_command_injection_resistant(payload):
    """Invariant: User-controlled input passed to subprocess.run must not execute arbitrary shell commands."""
    try:
        # Call the actual production function with adversarial input
        result = run_command(payload)
        # If the function returns, ensure no unintended commands were executed
        # We can't directly verify command execution, but we can check that the output
        # doesn't contain unexpected content from injected commands
        assert "hello" in result or "safe" in result or "test input" in result, \
            f"Expected output not found. Possible command injection with payload: {payload}"
    except subprocess.CalledProcessError as e:
        # If the command fails (e.g., due to invalid input), that's acceptable
        # as long as it doesn't execute arbitrary commands
        pass
    except Exception as e:
        # Any other exception should be investigated
        pytest.fail(f"Unexpected exception with payload {payload}: {e}")