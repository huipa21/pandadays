ALTER TABLE days ADD COLUMN calendar TEXT NOT NULL DEFAULT 'gregorian'
    CHECK (calendar IN ('gregorian', 'lunar'));