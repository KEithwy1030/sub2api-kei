-- Prevent OpenAI 429 quota responses from downgrading paid OAuth accounts to free.
-- A 429 usage-limit response means the quota window is exhausted; it does not
-- prove that the account subscription changed.
CREATE OR REPLACE FUNCTION public.prevent_openai_paid_plan_downgrade_on_429()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  old_plan text;
  new_plan text;
BEGIN
  old_plan := lower(trim(coalesce(OLD.credentials->>'plan_type', '')));
  new_plan := lower(trim(coalesce(NEW.credentials->>'plan_type', '')));

  IF OLD.platform = 'openai'
     AND OLD.type = 'oauth'
     AND old_plan IN ('plus', 'pro', 'team', 'business', 'enterprise', 'edu')
     AND new_plan = 'free' THEN
    NEW.credentials := jsonb_set(coalesce(NEW.credentials, '{}'::jsonb), '{plan_type}', to_jsonb(old_plan), true);
  END IF;

  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_prevent_openai_paid_plan_downgrade_on_429 ON public.accounts;
CREATE TRIGGER trg_prevent_openai_paid_plan_downgrade_on_429
BEFORE UPDATE OF credentials ON public.accounts
FOR EACH ROW
EXECUTE FUNCTION public.prevent_openai_paid_plan_downgrade_on_429();
