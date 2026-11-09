-- The exercise library by body part, and each person's own plan per body part.
--
-- body_part: what the app groups exercises by (back, biceps, triceps, chest, legs, abs,
--            shoulders). muscle_group stays as it was, for the web app.
-- motion:    which animation the app plays for the exercise (see the app's
--            exercise_motion.dart). 'none' means no animation.
-- muscles:   what it trains, in words.

ALTER TABLE exercises
    ADD COLUMN body_part text,
    ADD COLUMN motion    text NOT NULL DEFAULT 'none',
    ADD COLUMN muscles   text NOT NULL DEFAULT '';

INSERT INTO exercises (name, muscle_group, equipment, body_part, motion, muscles) VALUES
    -- back
    ('Deadlift',                   'back',      'barbell',    'back',      'deadlift',          'Lower back, glutes, hamstrings'),
    ('Pull-Up',                    'back',      'bodyweight', 'back',      'pullup',            'Lats, biceps'),
    ('Barbell Row',                'back',      'barbell',    'back',      'row',               'Lats, rhomboids, rear delts'),
    ('One-Arm Dumbbell Row',       'back',      'dumbbell',   'back',      'row',               'Lats, rhomboids'),
    ('T-Bar Row',                  'back',      'barbell',    'back',      'row',               'Mid back, lats'),
    ('Lat Pulldown',               'back',      'cable',      'back',      'pulldown',          'Lats, biceps'),
    ('Seated Cable Row',           'back',      'cable',      'back',      'cable_row',         'Mid back, lats'),
    ('Straight-Arm Pulldown',      'back',      'cable',      'back',      'straight_pulldown', 'Lats'),
    -- biceps
    ('Barbell Curl',               'arms',      'barbell',    'biceps',    'curl',              'Biceps'),
    ('Dumbbell Curl',              'arms',      'dumbbell',   'biceps',    'curl',              'Biceps'),
    ('Hammer Curl',                'arms',      'dumbbell',   'biceps',    'curl',              'Brachialis, forearms'),
    ('Cable Curl',                 'arms',      'cable',      'biceps',    'cable_curl',        'Biceps'),
    ('Preacher Curl',              'arms',      'barbell',    'biceps',    'preacher',          'Biceps, short head'),
    ('Concentration Curl',         'arms',      'dumbbell',   'biceps',    'concentration',     'Biceps, peak'),
    -- triceps
    ('Triceps Pushdown',           'arms',      'cable',      'triceps',   'pushdown',          'Triceps'),
    ('Skull Crusher',              'arms',      'barbell',    'triceps',   'skull',             'Triceps, long head'),
    ('Overhead Triceps Extension', 'arms',      'dumbbell',   'triceps',   'overhead_ext',      'Triceps, long head'),
    ('Close-Grip Bench Press',     'arms',      'barbell',    'triceps',   'bench',             'Triceps, chest'),
    ('Bench Dip',                  'arms',      'bodyweight', 'triceps',   'bench_dip',         'Triceps'),
    -- chest
    ('Barbell Bench Press',        'chest',     'barbell',    'chest',     'bench',             'Chest, triceps, front delts'),
    ('Incline Dumbbell Press',     'chest',     'dumbbell',   'chest',     'incline',           'Upper chest, front delts'),
    ('Dumbbell Fly',               'chest',     'dumbbell',   'chest',     'fly_lying',         'Chest'),
    ('Cable Fly',                  'chest',     'cable',      'chest',     'fly',               'Chest'),
    ('Push-Up',                    'chest',     'bodyweight', 'chest',     'pushup',            'Chest, triceps'),
    ('Chest Dip',                  'chest',     'bodyweight', 'chest',     'dip',               'Lower chest, triceps'),
    ('Machine Chest Press',        'chest',     'machine',    'chest',     'chest_press',       'Chest, triceps'),
    -- legs
    ('Back Squat',                 'legs',      'barbell',    'legs',      'squat',             'Quads, glutes'),
    ('Front Squat',                'legs',      'barbell',    'legs',      'squat',             'Quads, core'),
    ('Romanian Deadlift',          'legs',      'barbell',    'legs',      'rdl',               'Hamstrings, glutes'),
    ('Leg Press',                  'legs',      'machine',    'legs',      'leg_press',         'Quads, glutes'),
    ('Walking Lunge',              'legs',      'dumbbell',   'legs',      'lunge',             'Quads, glutes'),
    ('Bulgarian Split Squat',      'legs',      'dumbbell',   'legs',      'lunge',             'Quads, glutes'),
    ('Leg Extension',              'legs',      'machine',    'legs',      'leg_extension',     'Quads'),
    ('Leg Curl',                   'legs',      'machine',    'legs',      'leg_curl',          'Hamstrings'),
    ('Calf Raise',                 'legs',      'machine',    'legs',      'calf_raise',        'Calves'),
    -- abs
    ('Crunch',                     'core',      'bodyweight', 'abs',       'crunch',            'Upper abs'),
    ('Lying Leg Raise',            'core',      'bodyweight', 'abs',       'leg_raise',         'Lower abs'),
    ('Hanging Leg Raise',          'core',      'bodyweight', 'abs',       'hanging_leg_raise', 'Lower abs, hip flexors'),
    ('Cable Crunch',               'core',      'cable',      'abs',       'cable_crunch',      'Abs'),
    ('Plank',                      'core',      'bodyweight', 'abs',       'plank',             'Whole core'),
    -- shoulders
    ('Overhead Press',             'shoulders', 'barbell',    'shoulders', 'press',             'Front delts, triceps'),
    ('Dumbbell Shoulder Press',    'shoulders', 'dumbbell',   'shoulders', 'press',             'Front and side delts'),
    ('Lateral Raise',              'shoulders', 'dumbbell',   'shoulders', 'lateral_raise',     'Side delts'),
    ('Front Raise',                'shoulders', 'dumbbell',   'shoulders', 'front_raise',       'Front delts'),
    ('Reverse Fly',                'shoulders', 'dumbbell',   'shoulders', 'reverse_fly',       'Rear delts'),
    ('Face Pull',                  'shoulders', 'cable',      'shoulders', 'face_pull',         'Rear delts, upper back')
ON CONFLICT (name) DO UPDATE SET
    muscle_group = EXCLUDED.muscle_group,
    equipment    = EXCLUDED.equipment,
    body_part    = EXCLUDED.body_part,
    motion       = EXCLUDED.motion,
    muscles      = EXCLUDED.muscles;

-- Anything else that was already there gets a body part from its muscle group.
UPDATE exercises SET body_part = CASE muscle_group
    WHEN 'core' THEN 'abs'
    WHEN 'arms' THEN 'biceps'
    ELSE muscle_group END
WHERE body_part IS NULL;

ALTER TABLE exercises
    ALTER COLUMN body_part SET NOT NULL,
    ADD CONSTRAINT exercises_body_part_check
        CHECK (body_part IN ('back', 'biceps', 'triceps', 'chest', 'legs', 'abs', 'shoulders'));

-- One person's plan for a body part is a list of exercises, each with its sets. Weights
-- are kept in the unit the person chose for that exercise (kg or lb).
CREATE TABLE plan_exercises (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL,
    body_part   text        NOT NULL,
    exercise_id uuid        NOT NULL REFERENCES exercises(id),
    position    integer     NOT NULL,
    unit        text        NOT NULL DEFAULT 'kg' CHECK (unit IN ('kg', 'lb')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, body_part, exercise_id) -- an exercise is in a plan at most once
);

CREATE INDEX plan_exercises_user_idx ON plan_exercises (user_id, body_part, position);

CREATE TABLE plan_sets (
    plan_exercise_id uuid             NOT NULL REFERENCES plan_exercises(id) ON DELETE CASCADE,
    position         integer          NOT NULL CHECK (position BETWEEN 1 AND 10), -- at most 10 sets
    weight           double precision NOT NULL CHECK (weight BETWEEN 0 AND 2000),
    reps             integer          NOT NULL CHECK (reps BETWEEN 0 AND 1000),
    PRIMARY KEY (plan_exercise_id, position)
);
