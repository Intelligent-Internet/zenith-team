import unittest

from profile_prompt import prompts


class ProfilePromptTests(unittest.TestCase):
    def test_context_is_opt_in_and_results_are_not_instructions(self):
        profile = {
            "name": "Helia",
            "title": "Secretary",
            "instructions": "",
            "settings": {"participation": "addressed"},
        }
        live, backend = prompts(profile)
        self.assertIn("Company context is not enabled", live)
        live, backend = prompts(profile, True)
        self.assertIn("retrieve current facts", live)
        self.assertIn("untrusted data", backend)
        self.assertIn("Saving the draft creates no Task or Run", backend)
        self.assertIn("Confirm and create Task", backend)
        self.assertIn("Confirmation saves a Task without starting a Run", backend)
        self.assertIn("You cannot confirm or submit on anyone's behalf", backend)
        self.assertIn("leave_meeting", backend)
        self.assertIn("Never infer an authorizing member", backend)
        self.assertIn("get_work_status", live)
        self.assertIn("get_work_result", backend)
        self.assertIn("Run completion is not Task acceptance", backend)
        self.assertIn("Leaving the call does not cancel", backend)

    def test_shared_instructions_and_identity_reach_reasoning(self):
        live, reasoning = prompts(
            {
                "name": "Eli",
                "title": "Engineer",
                "instructions": "Explain tradeoffs.",
                "settings": {"participation": "addressed"},
            }
        )
        self.assertIn("Eli", live)
        self.assertIn("Explain tradeoffs.", reasoning)
        self.assertIn("only when addressed", live)
        self.assertIn("no web", reasoning)

    def test_natural_participation_does_not_fill_silence(self):
        live, _ = prompts(
            {
                "name": "Helia",
                "title": "Secretary",
                "instructions": "",
                "settings": {"participation": "natural"},
            }
        )
        self.assertIn("natural pauses", live)
        self.assertIn("Avoid filling silence", live)
