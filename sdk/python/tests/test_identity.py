import unittest

from runbox import Identity, IdentityError


class IdentityTest(unittest.TestCase):
    def test_token_headers(self):
        identity = Identity(token="01J-token")
        self.assertEqual(identity.headers(), {"X-Runbox-Identity": "01J-token"})

    def test_scoped_headers(self):
        identity = Identity(namespace="team-a", principal="user-42")
        self.assertEqual(
            identity.headers(),
            {
                "X-Runbox-Namespace": "team-a",
                "X-Runbox-Principal": "user-42",
            },
        )

    def test_ambiguous_is_rejected(self):
        with self.assertRaises(IdentityError):
            Identity(token="alice", namespace="team-a", principal="alice")

    def test_partial_scoped_is_rejected(self):
        with self.assertRaises(IdentityError):
            Identity(namespace="team-a")

    def test_invalid_characters_are_rejected(self):
        with self.assertRaises(IdentityError):
            Identity(namespace="team a", principal="alice")
        with self.assertRaises(IdentityError):
            Identity(token="a b")


if __name__ == "__main__":
    unittest.main()
