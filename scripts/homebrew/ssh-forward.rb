# Snapshot of the live formula in the private tap
# wangnan0916/homebrew-ssh-forward (the tap is the source of truth; keep
# this file in sync and push it to the tap when it changes).
#
# The repository is private, so the release archive cannot be fetched
# anonymously: install with --HEAD, which clones the main branch over the
# user's git credentials.
#
#   brew tap wangnan0916/homebrew-ssh-forward
#   brew install --HEAD ssh-forward

class SshForward < Formula
  desc "Expose eligible Linux Development Host listeners locally through system OpenSSH"
  homepage "https://github.com/wangnan0916/ssh-forward"
  url "https://github.com/wangnan0916/ssh-forward/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "48f02daf25bc5a1f1f18e3a67066d4ea43ac51be38e6dcccec115ef8616a4701"
  head "https://github.com/wangnan0916/ssh-forward.git", branch: "main"
  depends_on "go" => :build

  def install
    cd "cli" do
      system "go", "build", "-o", bin/"ssh-forward", "./cmd/ssh-forward"
    end
  end

  test do
    assert_match "ssh-forward 0.1.0", shell_output("#{bin}/ssh-forward --version")
  end
end
