#!/usr/bin/env python3
"""Focused regression tests for signed Kind proof helpers (no cluster needed)."""
import unittest
from helm_kind_lifecycle import proof_pod, validate_staged


class SignedProofTests(unittest.TestCase):
    def test_staged_requires_expected_release_sequence_and_safe_artifact(self):
        state = dict(phase="staged", release="test", accepted_sequence=1,
                     artifact="/state/staged/test.tar")
        self.assertEqual(validate_staged(state, "test"), state["artifact"])
        for field, bad in (("phase", "downloading"), ("release", "other"),
                           ("accepted_sequence", 2), ("artifact", "/origin/test.tar"),
                           ("artifact", "/state/../keys/publisher.pub")):
            with self.subTest(field=field, bad=bad), self.assertRaises(AssertionError):
                validate_staged(dict(state, **{field: bad}), "test")

    def test_proof_pod_has_no_ambient_credentials_or_extra_mounts(self):
        volumes = [{"name": "public", "configMap": {"name": "publisher-public"}}]
        mounts = [{"name": "public", "mountPath": "/keys", "readOnly": True}]
        spec = proof_pod("client", "local:test", ["edgelab", "watch"], volumes, mounts)["spec"]
        self.assertFalse(spec["automountServiceAccountToken"])
        self.assertEqual(spec["restartPolicy"], "Never")
        self.assertEqual(spec["securityContext"]["runAsUser"], 100)
        self.assertTrue(spec["securityContext"]["runAsNonRoot"])
        self.assertEqual(spec["volumes"], volumes)
        container = spec["containers"][0]
        self.assertEqual(container["volumeMounts"], mounts)
        self.assertEqual(container["imagePullPolicy"], "Never")
        self.assertFalse(container["securityContext"]["allowPrivilegeEscalation"])
        self.assertEqual(container["securityContext"]["capabilities"], {"drop": ["ALL"]})


if __name__ == "__main__":
    unittest.main()
