# Snapshot of the live formula in wangnan0916/homebrew-ssh-forward
# (the tap is the source of truth; keep this file in sync and push it
# to the tap when it changes).
#
# Pre-1.0 installs from HEAD:
#
#   brew install --HEAD wangnan0916/ssh-forward/ssh-forward

class SshForward < Formula
  desc "Forward Linux development-host ports to localhost through system OpenSSH"
  homepage "https://github.com/wangnan0916/ssh-forward"
  head "https://github.com/wangnan0916/ssh-forward.git", branch: "main"
  depends_on "go" => :build

  def install
    cd "cli" do
      system "go", "build", "-o", bin/"ssh-forward", "./cmd/ssh-forward"
    end
  end

  test do
    assert_match "ssh-forward 0.1.0-alpha", shell_output("#{bin}/ssh-forward --version")
  end
end
