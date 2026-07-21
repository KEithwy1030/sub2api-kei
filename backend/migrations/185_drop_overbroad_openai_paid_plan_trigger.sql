-- A legacy custom migration installed this trigger while handling an OpenAI
-- 429, but the trigger affected every later credentials update and could hide
-- a real paid-to-free subscription change. Current quota handling no longer
-- needs a database-wide rewrite rule.
DROP TRIGGER IF EXISTS trg_prevent_openai_paid_plan_downgrade_on_429 ON public.accounts;
DROP FUNCTION IF EXISTS public.prevent_openai_paid_plan_downgrade_on_429();
